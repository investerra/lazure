package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// RestartFlags are the flags for `lazure restart`.
func RestartFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "revision", Usage: "target revision (default: current latestRevisionName)"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt"},
		&cli.BoolFlag{Name: "wait", Usage: "wait until all replicas have been replaced and are Ready"},
		&cli.DurationFlag{Name: "wait-timeout", Value: 5 * time.Minute, Usage: "max wait time (default: 5m)"},
		&cli.BoolFlag{Name: "logs", Value: true, Usage: "stream logs from the first new replica during --wait (--logs=false to disable)"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors in streamed logs (also honored via NO_COLOR env)"},
	}
}

// restartPollInterval is how often the --wait loop checks replica status.
// ACA typically takes 10-30 s for a replica restart, so 3 s is a good
// balance between snappy feedback and not hammering the ARM API.
const restartPollInterval = 3 * time.Second

// Restart implements `lazure restart <env>`. POSTs to the ACA
// restart-revision endpoint — fire-and-forget, no async polling
// because Azure's 202 carries no meaningful completion signal
// (the revision name doesn't change on restart).
//
// Defaults to the currently-active revision (latestRevisionName)
// when --revision is omitted.
func Restart(ctx context.Context, c *cli.Command) error {
	rev := c.String("revision")
	yes := c.Bool("yes")
	wait := c.Bool("wait")
	waitTimeout := c.Duration("wait-timeout")
	streamLogs := c.Bool("logs")
	color := shouldColor(c.Bool("no-color"))

	t, err := loadAzureTarget(c, "restart")
	if err != nil {
		return err
	}
	slog.Debug("restart: start",
		"env", t.Env, "revision", rev, "yes", yes, "wait", wait,
		"wait_timeout", waitTimeout, "logs", streamLogs)

	if rev == "" {
		rev, err = resolveLatestRevision(ctx, t, "restart")
		if err != nil {
			return err
		}
	}

	if !yes {
		fmt.Printf("\nrestart revision %s\n  env:      %s\n  sub:      %s\n  rg:       %s\n",
			rev, t.Env, t.SubLabel(), t.RG)
		if !promptConfirm("proceed?") {
			return errs.Usage(errs.New("restart: aborted by user"))
		}
	}

	// Snapshot the baseline replica names BEFORE restart so --wait
	// can detect when they've all been replaced.
	var baseline map[string]struct{}
	if wait {
		initial, err := t.CA.ListReplicas(ctx, t.Sub, t.RG, t.Name, rev)
		if err != nil {
			return errs.System(errs.Wrap(err, "restart: list replicas (baseline for --wait)"))
		}
		baseline = replicaNameSet(initial)
		slog.Debug("restart: captured baseline replicas", "count", len(baseline))
	}

	slog.Info("restarting revision", "app", t.Name, "revision", rev, "env", t.Env, "sub", t.SubLabel())
	if err := t.CA.RestartRevision(ctx, t.Sub, t.RG, t.Name, rev); err != nil {
		return errs.System(errs.Wrap(err, "restart"))
	}

	if !wait {
		slog.Info("restart triggered — replicas will cycle over the next ~30s (use --wait to block)")
		return nil
	}

	start := time.Now()
	if err := waitForRestart(ctx, t.CA, t.Sub, t.RG, t.Name, rev, baseline, waitTimeout, streamLogs, color); err != nil {
		return errs.System(errs.Wrap(err, "restart: --wait"))
	}
	slog.Info("restart complete — all replicas replaced and Ready",
		"app", t.Name, "revision", rev,
		"duration", time.Since(start).Round(time.Second))
	return nil
}

// replicaNameSet turns a slice of replicas into a set of their names
// for O(1) membership checks during --wait polling.
func replicaNameSet(replicas []azurearm.Replica) map[string]struct{} {
	out := make(map[string]struct{}, len(replicas))
	for _, r := range replicas {
		out[r.Name] = struct{}{}
	}
	return out
}

// waitForRestart polls ACA's replica list until every replica in the
// baseline has been replaced (new names) AND all current replicas have
// all their containers Ready. This is the best-available signal that a
// restart is truly done: Azure's 202 on the restart endpoint gives no
// completion hook.
//
// Shows a TTY spinner with elapsed/remaining countdown while waiting;
// auto-noops to plain polling when stderr isn't a terminal (CI, pipes).
//
// When streamLogs is true, the first new replica to reach Ready state
// becomes the log-stream target: the spinner stops (logs are the more
// informative progress signal from that point) and a goroutine follows
// that replica's logs to stdout until the restart completes. ctx is
// cancelled on completion so the stream returns cleanly.
//
// Returns ctx.Err() if the context is cancelled, or a timeout error if
// the deadline is hit without seeing all-new + all-Ready state.
func waitForRestart(ctx context.Context, ca *azureapi.ContainerAppsClient,
	sub, rg, name, rev string, baseline map[string]struct{}, timeout time.Duration,
	streamLogs, color bool,
) error {
	return pollUntilReadyWithLogs(ctx, ca, sub, rg, name, rev, timeout, streamLogs, color, waitConfig{
		LogPrefix:  "restart",
		InitialMsg: "waiting for replicas to restart",
		TimeoutMsg: fmt.Sprintf("timed out after %s waiting for replicas to restart", timeout),
		DoneFn: func(replicas []azurearm.Replica) bool {
			done, _ := restartComplete(replicas, baseline)
			return done
		},
		StatusMsgFn: func(replicas []azurearm.Replica) string {
			_, status := restartComplete(replicas, baseline)
			return spinnerMessage(status)
		},
		PickStreamFn: func(replicas []azurearm.Replica) (azurearm.Replica, bool) {
			return findFirstNewReadyReplica(replicas, baseline)
		},
	})
}

// findFirstNewReadyReplica returns the first replica in `current` that
// is NOT in `baseline` AND has all its containers Ready. Returns
// (zero, false) if no such replica exists — caller loops and checks
// again next tick. Iteration order mirrors the input so the choice is
// deterministic relative to Azure's response ordering.
func findFirstNewReadyReplica(current []azurearm.Replica, baseline map[string]struct{}) (azurearm.Replica, bool) {
	for _, r := range current {
		if _, wasBaseline := baseline[r.Name]; wasBaseline {
			continue
		}
		if replicaAllContainersReady(r) {
			return r, true
		}
	}
	return azurearm.Replica{}, false
}

// spinnerMessage renders the current poll status as a compact string
// for the spinner line. Separates phases so users can see progress:
//
//	initial:       "waiting for Azure to cycle replicas"
//	cycling:       "1 old replica still terminating, 2/3 new replicas ready"
//	almost there:  "2/3 new replicas ready"
func spinnerMessage(s restartStatus) string {
	if s.newTotal == 0 && s.baselineStillPresent == 0 {
		return "waiting for Azure to cycle replicas"
	}
	if s.baselineStillPresent > 0 {
		return fmt.Sprintf("%d old replica(s) still terminating, %d/%d new ready",
			s.baselineStillPresent, s.newReady, s.newTotal)
	}
	return fmt.Sprintf("%d/%d new replicas ready", s.newReady, s.newTotal)
}

// restartStatus is the breakdown of a single poll — used both for the
// done check and for debug-level visibility into what's left.
type restartStatus struct {
	baselineStillPresent int // old replicas still around
	newReady             int // new replicas with all containers Ready
	newTotal             int // new replicas regardless of readiness
}

// restartComplete reports whether `current` represents a fully-restarted
// state: no baseline replicas remain AND every current replica is Ready.
//
// Pure function so it can be unit-tested without hitting Azure.
func restartComplete(current []azurearm.Replica, baseline map[string]struct{}) (bool, restartStatus) {
	var status restartStatus
	if len(current) == 0 {
		// ACA briefly returns empty lists during restart — treat as
		// not-done so we keep polling until real replicas appear.
		return false, status
	}
	for _, r := range current {
		if _, wasBaseline := baseline[r.Name]; wasBaseline {
			status.baselineStillPresent++
			continue
		}
		status.newTotal++
		if replicaAllContainersReady(r) {
			status.newReady++
		}
	}
	done := status.baselineStillPresent == 0 && status.newReady == status.newTotal && status.newTotal > 0
	return done, status
}

// replicaAllContainersReady is true iff every container + init container
// in the replica has Ready=true. Returns false on an empty replica
// (shouldn't happen but defensive).
func replicaAllContainersReady(r azurearm.Replica) bool {
	if len(r.Properties.Containers) == 0 {
		return false
	}
	for _, cnt := range r.Properties.Containers {
		if !cnt.Ready {
			return false
		}
	}
	for _, cnt := range r.Properties.InitContainers {
		// Init containers complete — they're Ready once they've run to
		// completion. If they never became Ready, something's wrong.
		if !cnt.Ready {
			return false
		}
	}
	return true
}

// ---------- revision-ready wait (shared by deploy + rollback) ----------

// waitForRevisionReady polls ListReplicas(rev) until every replica of
// the given revision has all containers Ready. Used by `deploy --wait`
// and `rollback --wait` to confirm the new/target revision is truly
// serving rather than just "Azure accepted the request".
//
// Semantics differ from waitForRestart:
//   - No baseline diff — all replicas under `rev` are by definition
//     "the new ones" for deploy, or "the target ones" for rollback.
//   - Completion is "every replica Ready" (with len > 0). The 0-replica
//     case stays non-done so we keep polling during warmup.
//
// Log-streaming behavior is identical to waitForRestart: first replica
// to be Ready becomes the pinned stream target, spinner stops, stream
// runs until ctx cancel.
func waitForRevisionReady(ctx context.Context, ca *azureapi.ContainerAppsClient,
	sub, rg, name, rev string, timeout time.Duration,
	streamLogs, color bool,
) error {
	return pollUntilReadyWithLogs(ctx, ca, sub, rg, name, rev, timeout, streamLogs, color, waitConfig{
		LogPrefix:    "wait-revision",
		InitialMsg:   "waiting for " + rev + " replicas to be ready",
		TimeoutMsg:   fmt.Sprintf("timed out after %s waiting for %s replicas to be ready", timeout, rev),
		DoneFn:       allRevisionReplicasReady,
		StatusMsgFn:  revisionReadyMessage,
		PickStreamFn: findFirstReadyReplica,
	})
}

// findFirstReadyReplica returns the first replica whose containers are
// all Ready, or (zero, false) if none qualify. Simpler than
// findFirstNewReadyReplica — no baseline to diff against because deploy
// and rollback operate on revisions whose replicas are all "new" from
// the command's perspective.
func findFirstReadyReplica(replicas []azurearm.Replica) (azurearm.Replica, bool) {
	for _, r := range replicas {
		if replicaAllContainersReady(r) {
			return r, true
		}
	}
	return azurearm.Replica{}, false
}

// allRevisionReplicasReady is the completion check for deploy/rollback
// --wait. Non-empty input + every replica Ready = done. An empty list
// stays non-done so we keep polling during Azure's warmup window (can
// take a few seconds before replicas appear in the list at all).
func allRevisionReplicasReady(replicas []azurearm.Replica) bool {
	if len(replicas) == 0 {
		return false
	}
	for _, r := range replicas {
		if !replicaAllContainersReady(r) {
			return false
		}
	}
	return true
}

// revisionReadyMessage produces the spinner status line for the
// wait-revision poll. Covers three phases: no replicas yet, partial
// readiness, or all-ready (a transient state right before the loop
// detects done and returns).
func revisionReadyMessage(replicas []azurearm.Replica) string {
	if len(replicas) == 0 {
		return "waiting for replicas to appear"
	}
	ready := 0
	for _, r := range replicas {
		if replicaAllContainersReady(r) {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d replicas ready", ready, len(replicas))
}
