package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/schema"
)

// TestSchema_DefaultWritesToDir — `lazure schema` with no positional
// arg should write to <--dir>/deploy.schema.json.
func TestSchema_DefaultWritesToDir(t *testing.T) {
	dir := t.TempDir()
	runSchema(t, dir, "")

	body, err := os.ReadFile(filepath.Join(dir, "deploy.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimRight(body, "\n"), bytes.TrimRight(schema.JSON, "\n")) {
		t.Error("written bytes don't match embedded schema")
	}
	if body[len(body)-1] != '\n' {
		t.Error("schema file should end with a newline")
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("written file isn't valid JSON: %v", err)
	}
}

// TestSchema_ExplicitPath — positional arg overrides default location.
func TestSchema_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "nested", "sub")
	if err := os.MkdirAll(explicit, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(explicit, "custom.json")

	runSchema(t, dir, target)

	if _, err := os.Stat(target); err != nil {
		t.Errorf("explicit target not created: %v", err)
	}
}

// TestSchema_Stdout — `lazure schema -` writes to stdout instead of
// touching the filesystem. Captured via pipe to avoid polluting real
// test stdout.
func TestSchema_Stdout(t *testing.T) {
	dir := t.TempDir()

	out := captureStdoutRun(t, func() {
		runSchema(t, dir, "-")
	})
	// Default-dir file must NOT have been created when stdout was
	// explicitly requested.
	if _, err := os.Stat(filepath.Join(dir, "deploy.schema.json")); !os.IsNotExist(err) {
		t.Errorf("stdout mode should not create a file; stat err = %v", err)
	}
	if len(out) == 0 {
		t.Fatal("stdout output was empty")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout output isn't valid JSON: %v", err)
	}
}

// runSchema runs the Schema action with `--dir=dir` and optional
// positional path. Reuses urfave/cli so arg parsing mirrors production.
func runSchema(t *testing.T, dir, path string) {
	t.Helper()
	app := &cli.Command{
		Name:  "lazure",
		Flags: []cli.Flag{&cli.StringFlag{Name: "dir", Value: dir}},
		Commands: []*cli.Command{
			{
				Name:      "schema",
				Arguments: SchemaArgs(),
				Action:    Schema,
			},
		},
	}
	args := []string{"lazure", "--dir", dir, "schema"}
	if path != "" {
		args = append(args, path)
	}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("Schema run failed: %v", err)
	}
}

// TestWithTrailingNewline guards the small helper — on/off cases
// should both produce exactly one trailing newline.
func TestWithTrailingNewline(t *testing.T) {
	cases := map[string]string{
		"hello":   "hello\n",
		"hello\n": "hello\n",
		"":        "",
	}
	for in, want := range cases {
		got := withTrailingNewline([]byte(in))
		if string(got) != want {
			t.Errorf("withTrailingNewline(%q) = %q, want %q", in, got, want)
		}
	}
}

// Unused silencing — `io` import would go unused if we ever drop the
// stdout-capture helper. Keep the compile happy either way.
var _ io.Reader
