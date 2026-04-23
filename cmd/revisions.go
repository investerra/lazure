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

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
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
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("revisions: env argument is required"))
	}
	dir := c.String("dir")
	limit := int(c.Int("limit"))
	format := c.String("format")
	slog.Debug("revisions: start", "env", env, "limit", limit, "format", format)

	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "revisions: load manifest"))
	}
	sub := manifest.App.Identity.SubscriptionID()
	if sub == "" {
		return errs.Usage(errs.Errorf("revisions: could not derive subscription id from app.identity %q", manifest.App.Identity))
	}
	rg, name := manifest.App.ResourceGroup, manifest.App.Name

	tokens, err := azureapi.NewTokenProvider()
	if err != nil {
		return errs.Auth(errs.Wrap(err, "revisions: auth"))
	}
	ca := azureapi.NewContainerAppsClient(tokens)

	revs, err := ca.ListRevisions(ctx, sub, rg, name)
	if err != nil {
		return errs.System(errs.Wrap(err, "revisions: list"))
	}
	if limit > 0 && len(revs) > limit {
		revs = revs[:limit]
	}

	switch format {
	case "", "table":
		return printRevisionsTable(env, name, revs)
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
