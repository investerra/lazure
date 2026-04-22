// Package logging configures slog with a human-friendly styled handler
// (via github.com/charmbracelet/log) for text mode and the stdlib JSON
// handler for structured/CI mode.
package logging

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
)

// Setup configures slog.Default() based on the user's --log-level and
// --log-format flags. Returns an error for unrecognized values.
//
// Text mode: charmbracelet/log as the slog handler with lipgloss styles
// — level colors plus bold/color emphasis on important keys (app, env,
// revision, secret, duration, error).
//
// JSON mode: stdlib slog.NewJSONHandler — one line per event, no colors,
// CI-friendly.
func Setup(level, format string) error {
	lvl, err := parseLevel(level)
	if err != nil {
		return err
	}

	var handler slog.Handler
	switch format {
	case "text", "":
		handler = newTextHandler(lvl)
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	default:
		return fmt.Errorf("invalid log-format %q (want text|json)", format)
	}

	slog.SetDefault(slog.New(handler))
	return nil
}

func parseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log-level %q (want debug|info|warn|error)", s)
	}
}

// newTextHandler builds a charmbracelet/log handler with styles per key.
// Keys worth emphasizing across lazure: app, env, revision, secret,
// container, dur / duration, error.
func newTextHandler(lvl slog.Level) slog.Handler {
	h := log.NewWithOptions(os.Stderr, log.Options{
		ReportTimestamp: true,
		TimeFormat:      "15:04:05",
		Level:           log.Level(lvl),
	})
	h.SetStyles(customStyles())
	return h
}

func customStyles() *log.Styles {
	s := log.DefaultStyles()

	// Keys that flag important context. Bold so they stand out in a wall of
	// INFO lines; color-coded so you can scan for the dimension you care
	// about (all env="prod" lines in red-orange, all revision="..."
	// lines in green, etc.).
	bold := lipgloss.NewStyle().Bold(true)
	emphasis := func(fg lipgloss.Color) lipgloss.Style {
		return lipgloss.NewStyle().Bold(true).Foreground(fg)
	}

	// Cyan — subjects
	s.Keys["app"] = emphasis(lipgloss.Color("87"))
	s.Values["app"] = bold
	s.Keys["container"] = emphasis(lipgloss.Color("87"))
	s.Values["container"] = bold

	// Yellow/orange — environment (prod draws the eye)
	s.Keys["env"] = emphasis(lipgloss.Color("214"))
	s.Values["env"] = bold

	// Green — revisions / versions
	s.Keys["revision"] = emphasis(lipgloss.Color("42"))
	s.Values["revision"] = bold

	// Magenta — secret names (reminds you it's a credential-adjacent value)
	s.Keys["secret"] = emphasis(lipgloss.Color("213"))
	s.Values["secret"] = bold

	// Red — errors
	s.Keys["error"] = emphasis(lipgloss.Color("196"))
	s.Values["error"] = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	s.Keys["err"] = s.Keys["error"]
	s.Values["err"] = s.Values["error"]

	// Dim — timing/auxiliary info that shouldn't steal focus
	dim := lipgloss.NewStyle().Faint(true)
	s.Keys["dur"] = dim
	s.Values["dur"] = dim
	s.Keys["duration"] = dim
	s.Values["duration"] = dim

	return s
}
