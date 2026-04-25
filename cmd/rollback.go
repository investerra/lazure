package cmd

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// RollbackFlags are the flags for `lazure rollback`.
func RollbackFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "to", Usage: "target revision (required with -y; otherwise an interactive picker is shown)"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt"},
		&cli.BoolFlag{Name: "wait", Usage: "after traffic shifts, wait until the target revision's replicas are Ready"},
		&cli.DurationFlag{Name: "wait-timeout", Value: 5 * time.Minute, Usage: "max wait time (default: 5m)"},
		&cli.BoolFlag{Name: "logs", Value: true, Usage: "stream logs from the first ready replica during --wait (--logs=false to disable)"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors in streamed logs (also honored via NO_COLOR env)"},
	}
}

// rollbackPickerSize caps the interactive picker to the most recent N
// non-current revisions. Keeps the prompt manageable; users wanting
// something older pass --to explicitly.
const rollbackPickerSize = 5

// Rollback implements `lazure rollback <env>`. Shifts 100% of traffic
// at a previous revision via PATCH — does NOT deploy a new revision.
//
// Without --to, presents an interactive picker over the last N
// non-current revisions. With -y, --to is required (no prompt).
func Rollback(ctx context.Context, c *cli.Command) error {
	to := c.String("to")
	yes := c.Bool("yes")
	wait := c.Bool("wait")
	waitTimeout := c.Duration("wait-timeout")
	streamLogs := c.Bool("logs")
	color := shouldColor(c.Bool("no-color"))

	t, err := loadAzureTarget(c, "rollback")
	if err != nil {
		return err
	}
	slog.Debug("rollback: start",
		"env", t.Env, "to", to, "yes", yes, "wait", wait, "logs", streamLogs)

	slog.Debug("rollback: listing revisions")
	revs, err := t.CA.ListRevisions(ctx, t.Sub, t.RG, t.Name)
	if err != nil {
		return errs.System(errs.Wrap(err, "rollback: list revisions"))
	}
	current := findLatest(revs)
	slog.Debug("rollback: current revision", "revision", current)

	// Resolve target: --to or interactive picker.
	if to == "" {
		if yes {
			return errs.Usage(errs.New("rollback: --to is required with -y (non-interactive)"))
		}
		candidates := filterRollbackCandidates(revs, current, rollbackPickerSize)
		if len(candidates) == 0 {
			return errs.Usage(errs.New("rollback: no other revisions available to roll back to"))
		}
		to, err = promptSelectRevision(candidates)
		if err != nil {
			return err
		}
	}

	if to == current {
		return errs.Usage(errs.Errorf("rollback: target %q is already the current revision", to))
	}

	// Confirmation for -y-less path.
	if !yes {
		fmt.Printf("\nroll back traffic from %s → %s\n  env:  %s\n  sub:  %s\n  rg:   %s\n",
			current, to, t.Env, t.SubLabel(), t.RG)
		if !promptConfirm("proceed?") {
			return errs.Usage(errs.New("rollback: aborted by user"))
		}
	}

	traffic := []azurearm.TrafficEntry{
		{Weight: 100, RevisionName: to},
	}
	slog.Info("rolling back", "app", t.Name, "from", current, "to", to, "env", t.Env, "sub", t.SubLabel())
	start := time.Now()
	sp := newWaitSpinner(time.Time{})
	sp.SetMessage("ARM operation in progress")
	sp.Start()
	_, err = t.CA.PatchTrafficAndWait(ctx, t.Sub, t.RG, t.Name, traffic, "Single")
	sp.Stop()
	if err != nil {
		return errs.System(errs.Wrap(err, "rollback: patch traffic"))
	}

	slog.Info("rollback complete",
		"app", t.Name, "from", current, "to", to,
		"duration", time.Since(start).Round(time.Second))

	if wait {
		if err := waitForRevisionReady(ctx, t.CA, t.Sub, t.RG, t.Name, to, waitTimeout, streamLogs, color); err != nil {
			return errs.System(errs.Wrap(err, "rollback: --wait"))
		}
		slog.Info("rollback --wait complete — target revision's replicas Ready",
			"app", t.Name, "revision", to)
	}
	return nil
}

// filterRollbackCandidates returns up to `max` revisions, excluding the
// current one. Preserves input order (newest first per Azure's default).
func filterRollbackCandidates(revs []azurearm.Revision, current string, max int) []azurearm.Revision {
	out := make([]azurearm.Revision, 0, max)
	for _, r := range revs {
		if r.Name == current {
			continue
		}
		out = append(out, r)
		if len(out) >= max {
			break
		}
	}
	return out
}

// promptSelectRevision prints a numbered table of candidate revisions
// and reads a number from stdin. Returns the picked revision name or
// an error.
func promptSelectRevision(candidates []azurearm.Revision) (string, error) {
	fmt.Println("\navailable revisions to roll back to:")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tNAME\tAGE\tTRAFFIC\tSTATE")
	now := time.Now()
	for i, r := range candidates {
		age := "-"
		if !r.Properties.CreatedTime.IsZero() {
			age = humanAge(now.Sub(r.Properties.CreatedTime))
		}
		traffic := "-"
		if r.Properties.TrafficWeight > 0 {
			traffic = fmt.Sprintf("%d%%", r.Properties.TrafficWeight)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
			i+1, r.Name, age, traffic,
			stringOr(r.Properties.RunningState, "-"))
	}
	if err := tw.Flush(); err != nil {
		return "", errs.System(errs.Wrap(err, "rollback: flush table"))
	}

	fmt.Printf("\nenter number [1-%d] (or Ctrl-C to abort): ", len(candidates))
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", errs.Usage(errs.Wrap(err, "rollback: read stdin"))
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(candidates) {
		return "", errs.Usage(errs.Errorf("rollback: invalid selection %q", strings.TrimSpace(line)))
	}
	return candidates[idx-1].Name, nil
}
