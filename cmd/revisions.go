package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// RevisionsFlags are the flags for `lazure revisions`.
func RevisionsFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{Name: "limit", Usage: "max revisions to list (most recent first)", Value: 10},
		&cli.StringFlag{Name: "format", Usage: "output format: table|json", Value: "table"},
	}
}

// Revisions implements `lazure revisions <env>`. Table columns:
//
//	NAME (with "latest" marker) | AGE | TRAFFIC | REPLICAS | STATE | HEALTH
//
// Feeds the rollback picker.
func Revisions(ctx context.Context, c *cli.Command) error {
	limit := int(c.Int("limit"))
	format := c.String("format")

	t, err := loadAzureTarget(c, "revisions")
	if err != nil {
		return err
	}
	slog.Debug("revisions: start", "env", t.Env, "limit", limit, "format", format)

	revs, err := t.CA.ListRevisions(ctx, t.Sub, t.RG, t.Name)
	if err != nil {
		return errs.System(errs.Wrap(err, "revisions: list"))
	}
	if limit > 0 && len(revs) > limit {
		revs = revs[:limit]
	}

	switch format {
	case "", "table":
		return printRevisionsTable(t.Env, t.Name, revs)
	case "json":
		return printRevisionsJSON(revs)
	default:
		return errs.Usage(errs.Errorf("revisions: invalid --format %q (want table|json)", format))
	}
}

// printRevisionsTable emits the revision list as a tabwriter table.
// The revision currently serving traffic is tagged "(latest)" — there
// may be more than one "active" revision in Multiple mode, so we tag
// whichever has the highest traffic weight.
func printRevisionsTable(env, app string, revs []azurearm.Revision) error {
	latestName := findLatest(revs)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "# revisions for %s / %s\n", app, env)
	fmt.Fprintln(tw, "NAME\tAGE\tTRAFFIC\tREPLICAS\tSTATE\tHEALTH")

	now := time.Now()
	for _, rev := range revs {
		display := rev.Name
		if rev.Name == latestName {
			display += " (latest)"
		}
		age := "-"
		if !rev.Properties.CreatedTime.IsZero() {
			age = humanAge(now.Sub(rev.Properties.CreatedTime))
		}
		traffic := "-"
		if rev.Properties.TrafficWeight > 0 {
			traffic = fmt.Sprintf("%d%%", rev.Properties.TrafficWeight)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			display, age, traffic,
			rev.Properties.Replicas,
			stringOr(rev.Properties.RunningState, "-"),
			stringOr(rev.Properties.HealthState, "-"),
		)
	}
	return tw.Flush()
}

// findLatest returns the name of the revision currently serving the
// most traffic. Used to tag the table row.
func findLatest(revs []azurearm.Revision) string {
	name := ""
	best := -1
	for _, rev := range revs {
		if rev.Properties.TrafficWeight > best {
			best = rev.Properties.TrafficWeight
			name = rev.Name
		}
	}
	return name
}

func printRevisionsJSON(revs []azurearm.Revision) error {
	out, err := json.MarshalIndent(revs, "", "  ")
	if err != nil {
		return errs.System(errs.Wrap(err, "revisions: marshal"))
	}
	fmt.Println(string(out))
	return nil
}

// humanAge formats a duration as "5s", "12m", "3h", "4d". Prefers
// coarser units over precision — status tables care about "roughly
// how old" more than exact arithmetic.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func stringOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
