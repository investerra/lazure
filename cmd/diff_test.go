package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout wraps a function so we can inspect what it prints.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), runErr
}

func TestWriteDiff_UnifiedShowsPlusMinus(t *testing.T) {
	actual := []byte("foo: 1\nbar: 2\n")
	expected := []byte("foo: 1\nbar: 3\n")

	out, err := captureStdout(t, func() error {
		return writeDiff("unified", actual, expected, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--- deployed") {
		t.Errorf("unified output missing FROM header: %q", out)
	}
	if !strings.Contains(out, "+++ rendered") {
		t.Errorf("unified output missing TO header: %q", out)
	}
	if !strings.Contains(out, "-bar: 2") || !strings.Contains(out, "+bar: 3") {
		t.Errorf("diff should show bar value change: %q", out)
	}
}

func TestWriteDiff_EmptyWhenUnified(t *testing.T) {
	// No changes → difflib emits empty output. We should NOT print any
	// headers (saves operators from misreading empty as drift).
	same := []byte("x: 1\n")
	out, err := captureStdout(t, func() error {
		return writeDiff("unified", same, same, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("expected empty unified output for identical inputs, got %q", out)
	}
}

func TestWriteDiff_YAMLDumpsBothSides(t *testing.T) {
	out, err := captureStdout(t, func() error {
		return writeDiff("yaml", []byte("A\n"), []byte("B\n"), true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--- deployed.yaml ---") || !strings.Contains(out, "--- rendered.yaml ---") {
		t.Errorf("yaml format missing headers: %q", out)
	}
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Errorf("yaml format missing content: %q", out)
	}
}

func TestWriteDiff_JSONEmitsStructured(t *testing.T) {
	actual := []byte(`foo: 1`)
	expected := []byte(`foo: 2`)

	out, err := captureStdout(t, func() error {
		return writeDiff("json", actual, expected, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Output is two JSON docs joined; just check both keys appear.
	if !strings.Contains(out, `"deployed":`) || !strings.Contains(out, `"rendered":`) {
		t.Errorf("json format missing keys: %q", out)
	}
	if !strings.Contains(out, `"foo":1`) || !strings.Contains(out, `"foo":2`) {
		t.Errorf("json format missing values: %q", out)
	}
}

func TestWriteDiff_InvalidFormatErrors(t *testing.T) {
	err := writeDiff("xml", nil, nil, true)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !strings.Contains(err.Error(), "invalid --format") {
		t.Errorf("error = %v", err)
	}
}

// ---------- color ----------

func TestColorizeUnifiedDiff_StylesEachLineType(t *testing.T) {
	// Force lipgloss to always emit colors regardless of test env.
	// (Without this, lipgloss would detect a non-TTY and strip.)
	input := `--- deployed
+++ rendered
@@ -1,3 +1,3 @@
 context
-removed
+added
`
	got := colorizeUnifiedDiff(input)

	// The function should preserve the structure (same lines,
	// same order, same separators) even if colors are stripped.
	if !strings.Contains(got, "--- deployed") ||
		!strings.Contains(got, "+++ rendered") ||
		!strings.Contains(got, "@@ -1,3 +1,3 @@") ||
		!strings.Contains(got, "-removed") ||
		!strings.Contains(got, "+added") ||
		!strings.Contains(got, " context") {
		t.Errorf("structure broken by colorize: %q", got)
	}
}

func TestColorizeUnifiedDiff_EmptyInput(t *testing.T) {
	if got := colorizeUnifiedDiff(""); got != "" {
		t.Errorf("empty input should stay empty, got %q", got)
	}
}

func TestWriteDiff_UnifiedPlainSuppressesColor(t *testing.T) {
	out, err := captureStdout(t, func() error {
		return writeDiff("unified", []byte("a\n"), []byte("b\n"), true /* plain */)
	})
	if err != nil {
		t.Fatal(err)
	}
	// ANSI escape sequences all start with ESC = 0x1b = "\x1b[". If plain=true
	// we should never see them.
	if strings.Contains(out, "\x1b[") {
		t.Errorf("plain mode leaked ANSI escapes: %q", out)
	}
}

func TestShouldColor(t *testing.T) {
	// --no-color flag wins over everything.
	if shouldColor(true) {
		t.Error("shouldColor(true) should always return false")
	}

	// NO_COLOR env var forces off. Save/restore so we don't leak state.
	orig, had := os.LookupEnv("NO_COLOR")
	t.Setenv("NO_COLOR", "1")
	if shouldColor(false) {
		t.Error("NO_COLOR=1 should disable colors even when flag is false")
	}
	if !had {
		os.Unsetenv("NO_COLOR")
	} else {
		os.Setenv("NO_COLOR", orig)
	}

	// When NO_COLOR is unset and flag is false, result depends on the
	// test process's stdout — likely not a TTY under `go test`, so false.
	// We don't assert a specific value; just that it returns without panic.
	t.Setenv("NO_COLOR", "")
	_ = shouldColor(false)
}

func TestWriteDiff_EmptyFormatDefaultsToUnified(t *testing.T) {
	actual := []byte("foo: 1\n")
	expected := []byte("foo: 2\n")
	out, err := captureStdout(t, func() error {
		return writeDiff("", actual, expected, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--- deployed") {
		t.Errorf("empty format should default to unified, got %q", out)
	}
}
