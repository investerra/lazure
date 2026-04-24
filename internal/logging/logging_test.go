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
		{"warning", false}, // lazure-specific alias → warn
		{"error", false},
		{"DEBUG", false}, // slog.Level.UnmarshalText is case-insensitive
		{"debug+1", false},
		{"error-2", false},
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
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	if err := Setup("debug", "text"); err != nil {
		t.Fatal(err)
	}
	if slog.Default() == orig {
		t.Error("Setup did not replace slog.Default()")
	}

	slog.Debug("hello", "app", "test", "env", "unit")
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

func TestParseLevel_LazureAliases(t *testing.T) {
	// Non-stdlib inputs our parseLevel handles specially:
	//   ""        → info   (not an error — common default flow)
	//   "warning" → warn   (user-friendly synonym)
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"warning": slog.LevelWarn,
	}
	for in, want := range cases {
		got, err := parseLevel(in)
		if err != nil {
			t.Errorf("parseLevel(%q) err = %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestReplaceAttr_KnownKeysColorized walks the lazure well-known keys
// through replaceAttr and checks the returned attr isn't the input
// pointer — indicating tint.Attr wrapped it. We don't inspect the
// actual ANSI wrapping because tint's internal attr shape is an impl
// detail; the guarantee we care about is "these keys are colored."
func TestReplaceAttr_KnownKeysColorized(t *testing.T) {
	keys := []string{"app", "container", "env", "revision", "secret", "error", "err", "dur", "duration"}
	for _, k := range keys {
		in := slog.String(k, "x")
		out := replaceAttr(nil, in)
		// tint.Attr returns an Any-kind attr holding a tintAttr struct;
		// an unchanged return would keep Kind == KindString, so a Kind
		// change signals wrapping happened.
		if out.Value.Kind() == slog.KindString && out.Value.String() == "x" {
			t.Errorf("key %q passed through unchanged — expected color wrapping", k)
		}
	}
}

func TestReplaceAttr_UnknownKeyPassesThrough(t *testing.T) {
	in := slog.String("unknown_key", "value")
	out := replaceAttr(nil, in)
	if out.Value.Kind() != slog.KindString || out.Value.String() != "value" {
		t.Errorf("unknown keys should pass unchanged, got %+v", out)
	}
}

func TestReplaceAttr_NestedGroupPassesThrough(t *testing.T) {
	// tint still invokes ReplaceAttr on attrs inside groups; we
	// specifically don't color those because group semantics aren't
	// stable in lazure's logging style.
	in := slog.String("env", "prd")
	out := replaceAttr([]string{"nested"}, in)
	if out.Value.Kind() != slog.KindString || out.Value.String() != "prd" {
		t.Errorf("grouped attrs should pass unchanged, got %+v", out)
	}
}
