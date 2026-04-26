package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
)

// EventsFlags are the flags for `lazure events`.
func EventsFlags() []cli.Flag {
	return []cli.Flag{
		&cli.DurationFlag{Name: "since", Value: 24 * time.Hour, Usage: "lookback window (e.g. 1h, 6h, 24h, 7d max-90d)"},
		&cli.IntFlag{Name: "limit", Value: 50, Usage: "max events to display (Azure returns up to its page size)"},
		&cli.StringFlag{Name: "format", Value: "table", Usage: "output format: table|json"},
	}
}

// Events implements `lazure events <env>`. Queries ARM activity log
// for recent operations on the container app — handy as a quick
// audit trail without going to the portal.
//
// Default window is 24h, max 90d (Azure-imposed). Output is sorted
// newest-first with caller, operation, and success/failure status.
func Events(ctx context.Context, c *cli.Command) error {
	since := c.Duration("since")
	limit := int(c.Int("limit"))
	format := c.String("format")

	t, err := loadAzureTarget(c, "events")
	if err != nil {
		return err
	}
	resourceID := azureapi.ContainerAppResourceID(t.Sub, t.RG, t.Name)
	slog.Debug("events: query", "resource", resourceID, "since", since, "limit", limit)

	events, err := azureapi.ListActivityEvents(ctx, t.Tokens, t.Sub, resourceID, time.Now().Add(-since))
	if err != nil {
		return errs.System(errs.Wrap(err, "events: list"))
	}
	// Azure orders by timestamp DESC by default but we re-sort defensively
	// to keep the contract independent of API quirks.
	sort.Slice(events, func(i, j int) bool {
		return events[i].EventTimestamp.After(events[j].EventTimestamp)
	})
	if len(events) > limit {
		events = events[:limit]
	}

	switch format {
	case "", "table":
		return printEventsTable(events, t.Env)
	case "json":
		return printEventsJSON(events)
	default:
		return errs.Usage(errs.Errorf("events: invalid --format %q (want table|json)", format))
	}
}

func printEventsTable(events []azureapi.ActivityEvent, env string) error {
	if len(events) == 0 {
		fmt.Println("no recent events")
		return nil
	}
	fmt.Printf("activity log for env %q (newest first):\n", env)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tOPERATION\tSTATUS\tCALLER")
	for _, e := range events {
		op := e.OperationName.LocalizedValue
		if op == "" {
			op = e.OperationName.Value
		}
		status := e.Status.LocalizedValue
		if status == "" {
			status = e.Status.Value
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			e.EventTimestamp.Local().Format("2006-01-02 15:04:05"),
			op, status, stringOr(e.Caller, "—"))
	}
	return tw.Flush()
}

func printEventsJSON(events []azureapi.ActivityEvent) error {
	out, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return errs.System(errs.Wrap(err, "events: marshal"))
	}
	fmt.Println(string(out))
	return nil
}
