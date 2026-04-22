package logging

import (
	"log/slog"
	"strings"
	"testing"
)

func TestSetup_LevelParsing(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"debug", false},
		{"info", false},
		{"warn", false},
		{"warning", false},
		{"error", false},
		{"", false}, // empty defaults to info
		{"bogus", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			err := Setup(tc.in, "text")
			if (err != nil) != tc.wantErr {
				t.Errorf("Setup(%q, text) err = %v, wantErr = %v", tc.in, err, tc.wantErr)
			}
		})
	}
}

func TestSetup_FormatParsing(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"text", false},
		{"json", false},
		{"", false}, // empty defaults to text
		{"xml", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			err := Setup("info", tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("Setup(info, %q) err = %v, wantErr = %v", tc.in, err, tc.wantErr)
			}
		})
	}
}

// TestSetup_InstallsDefault is a smoke test: after Setup, slog.Default()
// should be the one we configured, and slog.Info() should go through it.
// We can't easily capture the output (it goes to os.Stderr) without
// refactoring Setup to accept an io.Writer, but we can verify the handler
// was swapped.
func TestSetup_InstallsDefault(t *testing.T) {
	// Capture original default so we can restore it for test isolation.
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	if err := Setup("debug", "text"); err != nil {
		t.Fatal(err)
	}
	if slog.Default() == orig {
		t.Error("Setup did not replace slog.Default()")
	}

	// Calling a handler at debug level with a custom handler shouldn't panic.
	slog.Debug("hello", "app", "test", "env", "unit")
}

// TestCustomStyles verifies the key-styling map is populated for the keys
// we care about. We don't inspect the style output (that would lock us to
// lipgloss internals) — just that SetStyles is covering the expected keys.
func TestCustomStyles(t *testing.T) {
	s := customStyles()
	for _, k := range []string{"app", "env", "revision", "secret", "error", "err", "container", "dur", "duration"} {
		if _, has := s.Keys[k]; !has {
			t.Errorf("customStyles missing key %q", k)
		}
	}
}

func TestParseLevel_Error(t *testing.T) {
	_, err := parseLevel("not-a-level")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "debug|info|warn|error") {
		t.Errorf("error should list valid values, got %q", err.Error())
	}
}
