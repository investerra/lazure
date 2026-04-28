package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
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
		&cli.BoolFlag{Name: "expand", Usage: "show correlation IDs and parsed Azure error details — note: Azure error messages can carry image-pull URLs with SAS tokens, connection strings, or Key Vault refs; avoid piping to shared logs/CI artifacts"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors in table output (also honored via NO_COLOR env)"},
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
	expand := c.Bool("expand")
	color := shouldColor(c.Bool("no-color"))

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
		return printEventsTable(events, t.Env, expand, color)
	case "json":
		return printEventsJSON(events)
	default:
		return errs.Usage(errs.Errorf("events: invalid --format %q (want table|json)", format))
	}
}

func printEventsTable(events []azureapi.ActivityEvent, env string, expand, color bool) error {
	if len(events) == 0 {
		fmt.Println("no recent events")
		return nil
	}
	fmt.Printf("activity log for env %q (newest first):\n", env)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tOPERATION\tSTATUS\tCALLER")
	for i, e := range events {
		if expand && i > 0 {
			fmt.Fprintln(tw)
		}
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
			op, colorStatus(status, color), stringOr(e.Caller, "—"))
		if expand {
			printExpandedEvent(tw, e, color)
		}
	}
	return tw.Flush()
}

type activityStatusMessage struct {
	Status string               `json:"status"`
	Error  *activityStatusError `json:"error,omitempty"`
}

type activityStatusError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details []activityStatusDetail `json:"details,omitempty"`
}

type activityStatusDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func printExpandedEvent(w io.Writer, e azureapi.ActivityEvent, color bool) {
	fmt.Fprintln(w, "  Details:")
	printExpandedKV(w, "correlation", e.CorrelationID, color)
	printExpandedKV(w, "operation", e.OperationID, color)
	printExpandedKV(w, "eventData", e.EventDataID, color)
	printExpandedKV(w, "level", e.Level, color)
	printExpandedKV(w, "resource", e.ResourceID, color)

	statusMessage := strings.TrimSpace(e.Properties.StatusMessage)
	if statusMessage == "" {
		return
	}
	var msg activityStatusMessage
	if err := json.Unmarshal([]byte(statusMessage), &msg); err != nil || msg.Error == nil {
		printExpandedKV(w, "statusMessage", statusMessage, color)
		return
	}
	printExpandedKV(w, "azureStatus", msg.Status, color)
	printExpandedKV(w, "error", msg.Error.Code, color)
	printExpandedKV(w, "message", msg.Error.Message, color)
	for _, d := range msg.Error.Details {
		text := d.Message
		if d.Code != "" && text != "" {
			text = d.Code + ": " + text
		} else if d.Code != "" {
			text = d.Code
		}
		printExpandedKV(w, "detail", text, color)
	}
}

func printExpandedKV(w io.Writer, key, value string, color bool) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(w, "    %-12s %s\n", key+":", colorEventValue(key, value, color))
}

func colorEventValue(key, value string, color bool) string {
	switch key {
	case "level", "azureStatus", "error", "statusMessage", "detail":
		return colorStatus(value, color)
	default:
		return colorize(value, styleLogExtras, color)
	}
}

func printEventsJSON(events []azureapi.ActivityEvent) error {
	out, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return errs.System(errs.Wrap(err, "events: marshal"))
	}
	fmt.Println(string(out))
	return nil
}
