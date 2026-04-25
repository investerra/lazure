package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/charmbracelet/lipgloss"
	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// LogsFlags are the flags for `lazure logs`.
func LogsFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "container", Usage: "container name (default: first non-init container)"},
		&cli.BoolFlag{Name: "follow", Aliases: []string{"f"}, Usage: "stream new lines as they arrive"},
		&cli.IntFlag{Name: "tail", Value: 20, Usage: "number of historical lines before live data (max 300)"},
		&cli.StringFlag{Name: "revision", Usage: "target revision (default: current latestRevisionName)"},
		&cli.StringFlag{Name: "replica", Usage: "target replica (default: first returned)"},
		&cli.BoolFlag{Name: "raw", Usage: "print raw lines without JSON parsing or color"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors (also honored via NO_COLOR env)"},
	}
}

// Logs implements `lazure logs <env>`. Resolves {revision, replica,
// container} — defaulting each to the first reasonable choice — then
// opens a chunked HTTPS stream to that container's logStreamEndpoint
// and pipes each line to stdout.
//
// Ctrl-C (SIGINT/SIGTERM) cancels the context, closes the stream
// cleanly, and returns nil. StreamLogs surfaces ctx.Canceled which we
// swallow here since it's the expected shutdown path.
func Logs(ctx context.Context, c *cli.Command) error {
	container := c.String("container")
	follow := c.Bool("follow")
	tail := c.Int("tail")
	rev := c.String("revision")
	replica := c.String("replica")
	raw := c.Bool("raw")
	color := !raw && shouldColor(c.Bool("no-color"))

	t, err := loadAzureTarget(c, "logs")
	if err != nil {
		return err
	}
	slog.Debug("logs: start",
		"env", t.Env, "container", container, "follow", follow,
		"tail", tail, "revision", rev, "replica", replica,
		"raw", raw, "color", color)

	if rev == "" {
		rev, err = resolveLatestRevision(ctx, t, "logs")
		if err != nil {
			return err
		}
	}

	slog.Info("streaming logs",
		"app", t.Name, "env", t.Env, "revision", rev,
		"replica", replica, "container", container, "follow", follow, "tail", tail)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = streamContainerLogs(ctx, t.CA, t.Sub, t.RG, t.Name, rev, streamLogsOptions{
		Container: container,
		Replica:   replica,
		Follow:    follow,
		Tail:      tail,
		Raw:       raw,
		Color:     color,
		Out:       os.Stdout,
	})
	if errors.Is(err, context.Canceled) {
		slog.Debug("logs: stream cancelled (Ctrl-C or parent shutdown)")
		return nil
	}
	if err != nil {
		return errs.System(errs.Wrap(err, "logs: stream"))
	}
	return nil
}

// pickReplica returns the replica matching `name`, or the first one if
// name is empty. Errors if the list is empty or the name doesn't match.
func pickReplica(replicas []azurearm.Replica, name string) (azurearm.Replica, error) {
	if len(replicas) == 0 {
		return azurearm.Replica{}, errs.Errorf("no replicas found — is the revision scaled to zero?")
	}
	if name == "" {
		return replicas[0], nil
	}
	for _, r := range replicas {
		if r.Name == name {
			return r, nil
		}
	}
	names := make([]string, 0, len(replicas))
	for _, r := range replicas {
		names = append(names, r.Name)
	}
	return azurearm.Replica{}, errs.Errorf("replica %q not found; available: %v", name, names)
}

// pickContainer returns the named container from the replica, or the
// first non-init container if name is empty. Falls back to the first
// init container only if there are no regular containers (rare but
// possible for init-only replicas mid-startup).
func pickContainer(r azurearm.Replica, name string) (azurearm.ReplicaContainer, error) {
	all := append([]azurearm.ReplicaContainer{}, r.Properties.Containers...)
	all = append(all, r.Properties.InitContainers...)
	if len(all) == 0 {
		return azurearm.ReplicaContainer{}, errs.Errorf("replica %q has no containers", r.Name)
	}
	if name == "" {
		if len(r.Properties.Containers) > 0 {
			return r.Properties.Containers[0], nil
		}
		return r.Properties.InitContainers[0], nil
	}
	for _, c := range all {
		if c.Name == name {
			return c, nil
		}
	}
	names := make([]string, 0, len(all))
	for _, c := range all {
		names = append(names, c.Name)
	}
	return azurearm.ReplicaContainer{}, errs.Errorf("container %q not found on replica %q; available: %v", name, r.Name, names)
}

// ---------- line formatting ----------

// formatLogLine renders one ACA log line for display. Each raw line
// from Azure looks like `<RFC3339 timestamp> <payload>` where payload
// is either a structured JSON blob or a free-form text line.
//
// Behavior:
//   - raw=true: return the line unchanged (passthrough for piping / debug).
//   - JSON payload: extract timestamp/level/logger/message + remaining
//     fields and format them aligned with optional color.
//   - non-JSON payload: return the line unchanged (no heuristic coloring).
//
// Pure function — no side effects, no I/O — so it's unit-testable
// without spinning up mock Azure endpoints.
func formatLogLine(line string, raw, color bool) string {
	if raw {
		return line
	}
	acaTS, rest := splitACATimestamp(line)
	var payload map[string]any
	if err := json.Unmarshal([]byte(rest), &payload); err != nil || len(payload) == 0 {
		return line
	}
	return renderJSONLog(payload, acaTS, color)
}

// splitACATimestamp splits an ACA log line into its leading RFC3339
// timestamp and the rest. If there's no recognizable timestamp prefix
// (non-standard payload, or the stream already trimmed it) the whole
// line is returned as `rest` and `ts` is empty.
func splitACATimestamp(line string) (ts, rest string) {
	i := strings.IndexByte(line, ' ')
	if i <= 0 {
		return "", line
	}
	candidate := line[:i]
	// Cheap shape check: starts with 4 digits, ends with Z or Z-ish.
	if len(candidate) < 20 || candidate[4] != '-' || candidate[7] != '-' || candidate[10] != 'T' {
		return "", line
	}
	return candidate, line[i+1:]
}

// jsonLogFields is the set of keys formatLogLine promotes out of the
// payload into fixed columns. Everything else falls through to extras.
// Ordered aliases per column — first match wins. Lowercased keys only;
// payload lookup is case-sensitive in app logs, so we don't fold.
var jsonLogFields = struct {
	Timestamp []string
	Level     []string
	Logger    []string
	Message   []string
}{
	Timestamp: []string{"timestamp", "ts", "time", "@timestamp"},
	Level:     []string{"level", "severity", "lvl"},
	Logger:    []string{"logger", "name"},
	Message:   []string{"message", "msg"},
}

func renderJSONLog(payload map[string]any, acaTS string, color bool) string {
	ts := pickString(payload, jsonLogFields.Timestamp...)
	if ts == "" {
		ts = acaTS
	}
	level := pickString(payload, jsonLogFields.Level...)
	logger := pickString(payload, jsonLogFields.Logger...)
	msg := pickString(payload, jsonLogFields.Message...)

	consumed := make(map[string]struct{}, 8)
	markConsumed(consumed, jsonLogFields.Timestamp)
	markConsumed(consumed, jsonLogFields.Level)
	markConsumed(consumed, jsonLogFields.Logger)
	markConsumed(consumed, jsonLogFields.Message)
	extras := extraFields(payload, consumed)

	var b strings.Builder
	if ts != "" {
		b.WriteString(colorize(ts, styleLogTS, color))
		b.WriteByte(' ')
	}
	if level != "" {
		b.WriteString(colorize(padRight(strings.ToUpper(level), 5), levelStyle(level), color))
		b.WriteByte(' ')
	}
	if logger != "" {
		b.WriteString(colorize(logger, styleLogName, color))
		b.WriteString(": ")
	}
	b.WriteString(msg)
	if len(extras) > 0 {
		b.WriteByte(' ')
		b.WriteString(colorize(extras, styleLogExtras, color))
	}
	return b.String()
}

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
			if v != nil {
				return fmt.Sprint(v)
			}
		}
	}
	return ""
}

func markConsumed(set map[string]struct{}, keys []string) {
	for _, k := range keys {
		set[k] = struct{}{}
	}
}

// extraFields serializes non-promoted payload keys as `key=value` pairs
// in alphabetical order for stable output. Nested maps / slices are
// JSON-encoded compactly rather than spread across columns.
func extraFields(payload map[string]any, consumed map[string]struct{}) string {
	keys := make([]string, 0, len(payload))
	for k := range payload {
		if _, skip := consumed[k]; skip {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(formatExtraValue(payload[k]))
	}
	return b.String()
}

func formatExtraValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	case map[string]any, []any:
		enc, _ := json.Marshal(x)
		return string(enc)
	default:
		return fmt.Sprint(x)
	}
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func levelStyle(level string) lipgloss.Style {
	switch strings.ToLower(level) {
	case "debug", "trace":
		return styleLogTS // reuse dim
	case "info", "notice":
		return styleLogLevelInfo
	case "warn", "warning":
		return styleLogLevelWarn
	case "error", "err", "fatal", "critical", "alert", "emergency":
		return styleLogLevelError
	default:
		return styleLogName // default colored but not alarming
	}
}

func colorize(s string, style lipgloss.Style, color bool) string {
	if !color {
		return s
	}
	return style.Render(s)
}

// Soft palette matching cmd/diff.go for visual consistency.
var (
	styleLogTS         = lipgloss.NewStyle().Foreground(lipgloss.Color("241")) // dim gray
	styleLogName       = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // sage green
	styleLogExtras     = lipgloss.NewStyle().Foreground(lipgloss.Color("241")) // dim gray
	styleLogLevelInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("110")) // soft blue
	styleLogLevelWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("179")) // muted amber
	styleLogLevelError = lipgloss.NewStyle().Foreground(lipgloss.Color("174")) // soft coral
)
