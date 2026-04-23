package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
)

// RestartFlags are the flags for `lazure restart`.
func RestartFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "revision", Usage: "target revision (default: current latestRevisionName)"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt"},
		&cli.BoolFlag{Name: "wait", Usage: "wait until all replicas have been replaced and are Ready"},
		&cli.DurationFlag{Name: "wait-timeout", Value: 5 * time.Minute, Usage: "max wait time (default: 5m)"},
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
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("restart: env argument is required"))
	}
	dir := c.String("dir")
	rev := c.String("revision")
	yes := c.Bool("yes")
	wait := c.Bool("wait")
	waitTimeout := c.Duration("wait-timeout")
	slog.Debug("restart: start", "env", env, "revision", rev, "yes", yes, "wait", wait, "wait_timeout", waitTimeout)

	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "restart: load manifest"))
	}
	sub := manifest.App.Identity.SubscriptionID()
	if sub == "" {
		return errs.Usage(errs.Errorf("restart: could not derive subscription id from app.identity %q", manifest.App.Identity))
	}
	rg, name := manifest.App.ResourceGroup, manifest.App.Name

	tokens, err := azureapi.NewTokenProvider()
	if err != nil {
		return errs.Auth(errs.Wrap(err, "restart: auth"))
	}
	ca := azureapi.NewContainerAppsClient(tokens)

	// Default revision = current app's latestRevisionName.
	if rev == "" {
		app, err := ca.Get(ctx, sub, rg, name)
		switch {
		case errors.Is(err, azureapi.ErrContainerAppNotFound):
			return errs.Usage(errs.Errorf("restart: app %q not deployed yet", name))
		case err != nil:
			return errs.System(errs.Wrap(err, "restart: fetch current state"))
		}
		rev = app.Properties.LatestRevisionName
		if rev == "" {
			return errs.System(errs.New("restart: app has no latestRevisionName — is it still provisioning?"))
		}
		slog.Debug("restart: using current latest revision", "revision", rev)
	}

	if !yes {
		fmt.Printf("\nrestart revision %s in %s?\n", rev, env)
		if !promptConfirm("proceed?") {
			return errs.Usage(errs.New("restart: aborted by user"))
		}
	}

	// Snapshot the baseline replica names BEFORE restart so --wait
	// can detect when they've all been replaced.
	var baseline map[string]struct{}
	if wait {
		initial, err := ca.ListReplicas(ctx, sub, rg, name, rev)
		if err != nil {
			return errs.System(errs.Wrap(err, "restart: list replicas (baseline for --wait)"))
		}
		baseline = replicaNameSet(initial)
		slog.Debug("restart: captured baseline replicas", "count", len(baseline))
	}

	slog.Info("restarting revision", "app", name, "revision", rev, "env", env)
	if err := ca.RestartRevision(ctx, sub, rg, name, rev); err != nil {
		return errs.System(errs.Wrap(err, "restart"))
	}

	if !wait {
		slog.Info("restart triggered — replicas will cycle over the next ~30s (use --wait to block)")
		return nil
	}

	start := time.Now()
	if err := waitForRestart(ctx, ca, sub, rg, name, rev, baseline, waitTimeout); err != nil {
		return errs.System(errs.Wrap(err, "restart: --wait"))
	}
	slog.Info("restart complete — all replicas replaced and Ready",
		"app", name, "revision", rev,
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
// Returns ctx.Err() if the context is cancelled, or a timeout error if
// the deadline is hit without seeing all-new + all-Ready state.
func waitForRestart(ctx context.Context, ca *azureapi.ContainerAppsClient,
	sub, rg, name, rev string, baseline map[string]struct{}, timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	slog.Debug("restart: polling for replica replacement", "timeout", timeout)

	sp := newWaitSpinner(deadline)
	sp.SetMessage("waiting for replicas to restart")
	sp.Start()
	defer sp.Stop()

	for {
		if time.Now().After(deadline) {
			return errs.Errorf("timed out after %s waiting for replicas to restart", timeout)
		}

		select {
		case <-ctx.Done():
			return errs.Wrap(ctx.Err(), "restart: poll cancelled")
		case <-time.After(restartPollInterval):
		}

		current, err := ca.ListReplicas(ctx, sub, rg, name, rev)
		if err != nil {
			return errs.Wrap(err, "poll list replicas")
		}
		done, status := restartComplete(current, baseline)
		slog.Debug("restart: poll tick",
			"current_replicas", len(current),
			"baseline_remaining", status.baselineStillPresent,
			"new_ready", status.newReady,
			"new_total", status.newTotal,
			"done", done)

		sp.SetMessage(spinnerMessage(status))

		if done {
			return nil
		}
	}
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
