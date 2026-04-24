package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

// TestCompleteEnvs_WritesDiscoveredNames runs CompleteEnvs against a
// tempdir seeded with a handful of envs/*.vars.yml files, captures
// stdout, and verifies only the real env names are emitted — .plain
// and .secrets variants must be filtered by the underlying
// discoverEnvs helper.
func TestCompleteEnvs_WritesDiscoveredNames(t *testing.T) {
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	if err := os.Mkdir(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"dev.vars.yml",
		"uat.vars.yml",
		"prd.vars.yml",
		"dev.secrets.yml",      // noise — must be filtered
		"dev.secrets.plain.yml", // noise — must be filtered
	} {
		if err := os.WriteFile(filepath.Join(envsDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Minimal synthetic command with --dir set so CompleteEnvs can
	// read it. urfave/cli doesn't make it easy to construct a Command
	// without Run(); exercising the helper via a wrapped cli.Command
	// action avoids reaching into unexported fields.
	var captured string
	app := &cli.Command{
		Flags: []cli.Flag{&cli.StringFlag{Name: "dir"}},
		Action: func(ctx context.Context, c *cli.Command) error {
			captured = captureStdoutRun(t, func() { CompleteEnvs(ctx, c) })
			return nil
		},
	}
	if err := app.Run(context.Background(), []string{"lazure", "--dir", dir}); err != nil {
		t.Fatal(err)
	}

	got := strings.Fields(captured)
	sort.Strings(got)
	want := []string{"dev", "prd", "uat"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestCompleteEnvs_MissingEnvsDirSilent — no envs/ directory should
// produce empty output, not a panic or error. Shells calling the
// completion function must never see stderr noise from us.
func TestCompleteEnvs_MissingEnvsDirSilent(t *testing.T) {
	dir := t.TempDir() // no envs/ created

	var captured string
	app := &cli.Command{
		Flags: []cli.Flag{&cli.StringFlag{Name: "dir"}},
		Action: func(ctx context.Context, c *cli.Command) error {
			captured = captureStdoutRun(t, func() { CompleteEnvs(ctx, c) })
			return nil
		},
	}
	if err := app.Run(context.Background(), []string{"lazure", "--dir", dir}); err != nil {
		t.Fatal(err)
	}

	if strings.TrimSpace(captured) != "" {
		t.Errorf("expected empty output for missing envs dir, got %q", captured)
	}
}

// captureStdoutRun runs fn with os.Stdout redirected to a pipe and
// returns what was written. Kept local to avoid leaking a test helper
// into non-test code.
func captureStdoutRun(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
