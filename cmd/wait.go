package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// waitConfig parameterizes the per-call shape of pollUntilReadyWithLogs
// — what counts as "done", what the spinner says while waiting, and
// which replica becomes the live-stream target. Filled by callers
// (waitForRestart, waitForRevisionReady) with their distinct
// completion semantics.
type waitConfig struct {
	// LogPrefix scopes slog messages and warning text emitted from
	// inside the loop (e.g. "restart" vs "wait-revision").
	LogPrefix string

	// InitialMsg is the spinner line shown before the first poll tick.
	InitialMsg string

	// TimeoutMsg is the user-visible message on deadline expiry. The
	// caller pre-formats with the actual timeout duration so the
	// number is correct in the output.
	TimeoutMsg string

	// DoneFn returns true when the loop should terminate successfully.
	DoneFn func([]azurearm.Replica) bool

	// StatusMsgFn produces the spinner status line each tick (only
	// called while the log stream hasn't started yet).
	StatusMsgFn func([]azurearm.Replica) string

	// PickStreamFn picks a replica to follow logs from once one is
	// ready. Returns false if no replica is suitable yet — the loop
	// keeps trying next tick.
	PickStreamFn func([]azurearm.Replica) (azurearm.Replica, bool)
}

// pollUntilReadyWithLogs is the shared poll loop for both
// `restart --wait` (baseline diff) and `deploy/rollback --wait` (all
// replicas of a target revision). It owns the spinner lifecycle, the
// log-stream goroutine (when streamLogs is true), and the deadline /
// ctx-cancel teardown — all the moving parts that were previously
// duplicated between waitForRestart and waitForRevisionReady. Per-call
// behavior plugs in via waitConfig.
//
// Lifecycle:
//   - Spinner shows InitialMsg, then refreshes via StatusMsgFn each
//     tick until streaming starts.
//   - Once PickStreamFn returns a replica AND streamLogs is true, the
//     spinner stops, "streaming logs from X:" header prints to
//     stderr, and a goroutine pumps streamContainerLogs to stdout
//     until the loop returns.
//   - On return, streamCancel + 2s grace for the goroutine to flush
//     any in-flight lines before the caller's "complete" log line.
func pollUntilReadyWithLogs(
	ctx context.Context, ca *azureapi.ContainerAppsClient,
	sub, rg, name, rev string, timeout time.Duration,
	streamLogs, color bool,
	cfg waitConfig,
) error {
	deadline := time.Now().Add(timeout)
	slog.Debug(cfg.LogPrefix+": poll start",
		"revision", rev, "timeout", timeout, "stream_logs", streamLogs)

	sp := newWaitSpinner(deadline)
	sp.SetMessage(cfg.InitialMsg)
	sp.Start()
	defer sp.Stop()

	var (
		streamCtx, streamCancel = context.WithCancel(ctx)
		streamDone              = make(chan struct{})
		streamStarted           = false
	)
	defer func() {
		streamCancel()
		if streamStarted {
			// 2s grace for the goroutine to flush any in-flight lines
			// after streamCtx fires. Well over typical HTTP close
			// latency; users don't notice the wait at the end.
			select {
			case <-streamDone:
			case <-time.After(2 * time.Second):
			}
		}
	}()

	for {
		if time.Now().After(deadline) {
			return errs.New(cfg.TimeoutMsg)
		}

		select {
		case <-ctx.Done():
			return errs.Wrapf(ctx.Err(), "%s: poll cancelled", cfg.LogPrefix)
		case <-time.After(restartPollInterval):
		}

		replicas, err := ca.ListReplicas(ctx, sub, rg, name, rev)
		if err != nil {
			return errs.Wrap(err, "poll list replicas")
		}
		slog.Debug(cfg.LogPrefix+": poll tick",
			"replicas", len(replicas),
			"done", cfg.DoneFn(replicas))

		// Start streaming as soon as a replica qualifies. Logs become
		// the progress signal from here; spinner stops to avoid
		// garbling the live stream output.
		if streamLogs && !streamStarted {
			if target, ok := cfg.PickStreamFn(replicas); ok {
				streamStarted = true
				sp.Stop()
				fmt.Fprintf(os.Stderr, "\nstreaming logs from %s:\n", target.Name)
				go func(replicaName string) {
					defer close(streamDone)
					err := streamContainerLogs(streamCtx, ca, sub, rg, name, rev,
						streamLogsOptions{
							Replica: replicaName,
							Follow:  true,
							Tail:    0,
							Color:   color,
							Out:     os.Stdout,
						})
					if err != nil && !errors.Is(err, context.Canceled) {
						slog.Warn(cfg.LogPrefix+": log stream ended early", "err", err)
					}
				}(target.Name)
			}
		}

		if !streamStarted {
			sp.SetMessage(cfg.StatusMsgFn(replicas))
		}

		if cfg.DoneFn(replicas) {
			return nil
		}
	}
}
