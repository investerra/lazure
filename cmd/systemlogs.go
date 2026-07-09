package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
)

// systemLogsPollInterval is how often `logs --type system --follow`
// re-queries Log Analytics for new rows. Log Analytics ingestion
// itself lags by anywhere from seconds to a few minutes, so polling
// much faster than this doesn't surface data any sooner — it just
// hammers the query API.
const systemLogsPollInterval = 5 * time.Second

// systemLogsLookback bounds the initial query window before --tail
// truncates it. System logs are container/replica lifecycle events
// (image pulls, probe failures, restarts) — much lower volume than
// console output — so a generous window costs little.
const systemLogsLookback = 24 * time.Hour

// streamSystemLogsOptions configures a streamSystemLogs invocation,
// mirroring streamLogsOptions' shape for the console path.
type streamSystemLogsOptions struct {
	Revision string // "" → all revisions
	Replica  string // "" → all replicas
	Follow   bool
	Tail     int
	Raw      bool
	Color    bool
	Out      io.Writer
}

// streamSystemLogs resolves the container app's managed environment
// to a Log Analytics workspace, then queries ContainerAppSystemLogs_CL
// for platform-level events (image pulls, probe failures, OOM kills,
// container start/stop) — the table Azure keeps separate from
// ContainerAppConsoleLogs_CL (app stdout/stderr, what streamContainerLogs
// reads via the replica's logStreamEndpoint instead).
//
// Unlike the console path there's no push/streaming API for this
// table, so --follow is implemented as polling: each round queries
// rows newer than the last-seen timestamp and prints them.
func streamSystemLogs(ctx context.Context, t *azureTarget, opts streamSystemLogsOptions) error {
	if opts.Out == nil {
		return errs.Errorf("streamSystemLogs: opts.Out is required")
	}

	envID := t.Manifest.App.ManagedEnvironmentID
	env, err := t.CA.GetManagedEnvironment(ctx, envID)
	if err != nil {
		return errs.Wrapf(err, "systemlogs: get managed environment %s", envID)
	}
	logCfg := env.Properties.AppLogsConfiguration.LogAnalyticsConfiguration
	if logCfg == nil || logCfg.CustomerID == "" {
		return errs.Usage(errs.Errorf(
			"systemlogs: managed environment %q has no Log Analytics workspace configured (destination=%q) — system logs aren't queryable",
			envID, env.Properties.AppLogsConfiguration.Destination))
	}
	workspaceID := logCfg.CustomerID

	since := time.Now().Add(-systemLogsLookback)
	first := true
	for {
		rows, err := azureapi.QueryLogAnalytics(ctx, t.Tokens, workspaceID,
			containerAppSystemLogsQuery(t.Name, opts.Revision, opts.Replica, since))
		if err != nil {
			return errs.Wrap(err, "systemlogs: query")
		}
		if first && opts.Tail > 0 && len(rows) > opts.Tail {
			rows = rows[len(rows)-opts.Tail:]
		}
		first = false

		for _, row := range rows {
			fmt.Fprintln(opts.Out, formatSystemLogRow(row, opts.Raw, opts.Color))
			if ts, ok := parseSystemLogTimestamp(row); ok && ts.After(since) {
				since = ts
			}
		}

		if !opts.Follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(systemLogsPollInterval):
		}
	}
}

// containerAppSystemLogsQuery builds the KQL for one poll round.
// since is exclusive — advancing it to the last row's timestamp after
// each round is how --follow avoids re-printing rows, at the cost of
// a narrow window where two events sharing the same TimeGenerated
// value could see one skipped on the next round. Acceptable for a
// human-facing tail; Log Analytics doesn't offer cursor-based paging.
func containerAppSystemLogsQuery(appName, revision, replica string, since time.Time) string {
	var b strings.Builder
	b.WriteString("ContainerAppSystemLogs_CL")
	fmt.Fprintf(&b, "\n| where ContainerAppName_s == '%s'", kqlQuote(appName))
	fmt.Fprintf(&b, "\n| where TimeGenerated > datetime(%s)", since.UTC().Format(time.RFC3339Nano))
	if revision != "" {
		fmt.Fprintf(&b, "\n| where RevisionName_s == '%s'", kqlQuote(revision))
	}
	if replica != "" {
		fmt.Fprintf(&b, "\n| where ReplicaName_s == '%s'", kqlQuote(replica))
	}
	b.WriteString("\n| order by TimeGenerated asc")
	return b.String()
}

// kqlQuote escapes single quotes for use inside a KQL string literal.
// Revision/app names are ARM-restricted to alphanumerics and hyphens
// so this never actually fires in practice — it's a defensive guard
// against building a malformed (or injectable) query if that
// assumption ever breaks.
func kqlQuote(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

func parseSystemLogTimestamp(row azureapi.LogAnalyticsRow) (time.Time, bool) {
	s := systemLogField(row, "TimeGenerated")
	if s == "" {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// formatSystemLogRow renders one ContainerAppSystemLogs_CL row.
// raw=true dumps the full row as JSON (all columns, not just the ones
// we promote) — useful when Azure's table schema has fields this
// formatter doesn't know about yet.
func formatSystemLogRow(row azureapi.LogAnalyticsRow, raw, color bool) string {
	if raw {
		enc, err := json.Marshal(row)
		if err != nil {
			return fmt.Sprint(row)
		}
		return string(enc)
	}

	ts := systemLogField(row, "TimeGenerated")
	typ := systemLogField(row, "Type_s")
	reason := systemLogField(row, "Reason_s")
	replica := systemLogField(row, "ReplicaName_s")
	msg := systemLogField(row, "Log_s")

	var b strings.Builder
	if ts != "" {
		b.WriteString(colorize(ts, styleLogTS, color))
		b.WriteByte(' ')
	}
	if typ != "" {
		b.WriteString(colorize(padRight(strings.ToUpper(typ), 7), levelStyle(typ), color))
		b.WriteByte(' ')
	}
	if reason != "" {
		b.WriteString(colorize(reason, styleLogName, color))
		b.WriteString(": ")
	}
	if replica != "" {
		b.WriteString(colorize("["+replica+"] ", styleLogExtras, color))
	}
	if msg == "" {
		// Unrecognized schema — fall back to the whole row rather than
		// printing a blank line.
		enc, _ := json.Marshal(row)
		msg = string(enc)
	}
	b.WriteString(msg)
	return b.String()
}

func systemLogField(row azureapi.LogAnalyticsRow, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
