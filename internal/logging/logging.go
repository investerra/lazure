// Package logging configures slog with tint as the styled handler for
// text mode and the stdlib JSON handler for structured/CI mode.
//
// Text output uses lmittmann/tint — a small, zero-dependency slog
// handler with ANSI level colors built in and a ReplaceAttr hook we
// use for per-key coloring (app/env/revision/etc.).
//
// JSON output uses stdlib slog.NewJSONHandler — one event per line,
// no colors, ideal for CI log ingestion.
package logging

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/lmittmann/tint"
)

// Setup configures slog.Default() based on the user's --log-level and
// --log-format flags. Returns an error for unrecognized values.
//
// Level parsing uses slog.Level's stdlib UnmarshalText — accepts
// "debug", "info", "warn", "error" (case-insensitive), plus numeric
// offsets like "DEBUG+1" or "INFO-2" for fine-grained tuning.
func Setup(level, format string) error {
	lvl, err := parseLevel(level)
	if err != nil {
		return err
	}

	var handler slog.Handler
	switch format {
	case "text", "":
		handler = tint.NewHandler(os.Stderr, &tint.Options{
			Level:       lvl,
			TimeFormat:  "15:04:05",
			ReplaceAttr: replaceAttr,
		})
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	default:
		return fmt.Errorf("invalid log-format %q (want text|json)", format)
	}

	slog.SetDefault(slog.New(handler))
	return nil
}

// parseLevel wraps slog.Level.UnmarshalText with lazure-friendly
// defaults: empty string → info (not an error), "warning" → warn
// (backwards compat for users used to that spelling).
func parseLevel(s string) (slog.Level, error) {
	if s == "" {
		return slog.LevelInfo, nil
	}
	if s == "warning" {
		return slog.LevelWarn, nil
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("invalid log-level %q (want debug|info|warn|error, optionally with offset like debug+1)", s)
	}
	return lvl, nil
}

// replaceAttr applies per-key ANSI coloring to the lazure keys users
// want to scan for. Called once per attribute by tint before
// formatting; non-leaf attrs (nested groups) pass through unchanged
// because we don't style those.
//
// Colors match the charm-era palette for continuity: cyan subjects,
// orange env, green revision, magenta secret, red error, dim timing.
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case "app", "container":
		return tint.Attr(87, a) // cyan
	case "env":
		return tint.Attr(214, a) // orange
	case "revision":
		return tint.Attr(42, a) // green
	case "secret":
		return tint.Attr(213, a) // magenta
	case "error", "err":
		return tint.Attr(196, a) // red
	case "dur", "duration":
		return tint.Attr(240, a) // dim
	}
	return a
}
