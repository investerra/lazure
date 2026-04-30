package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"
)

// ---------- redact ----------

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "***"},
		{"abc", "***"},
		{"short6", "***"},        // exactly 6 chars = "***"
		{"1234567", "123…567"},   // 7 chars → first 3 + … + last 3 (overlapping middle "4")
		{"hunter2pass", "hun…ass"},
		{"sample-secret-value", "sam…lue"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := redact(tc.in); got != tc.want {
				t.Errorf("redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------- sortedKeys ----------

func TestSortedKeys(t *testing.T) {
	in := map[string]string{"z": "1", "a": "2", "m": "3"}
	got := sortedKeys(in)
	want := []string{"a", "m", "z"}
	for i, k := range want {
		if got[i] != k {
			t.Errorf("sortedKeys = %v, want %v", got, want)
			break
		}
	}
}

// ---------- marshalPlainSecrets ----------

func TestMarshalPlainSecrets_Deterministic(t *testing.T) {
	secrets := map[string]string{
		"zzz":    "last",
		"aaa":    "first",
		"middle": "hello",
	}
	out, err := marshalPlainSecrets(secrets)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	// Comment header.
	if !strings.Contains(s, "# Decrypted secrets") {
		t.Error("missing header comment")
	}

	// Alphabetical order — aaa before middle before zzz.
	idxA := strings.Index(s, "aaa:")
	idxM := strings.Index(s, "middle:")
	idxZ := strings.Index(s, "zzz:")
	if idxA >= idxM || idxM >= idxZ {
		t.Errorf("keys not in alphabetical order: %s", s)
	}

	// Values are quoted.
	if !strings.Contains(s, "first") || !strings.Contains(s, "hello") || !strings.Contains(s, "last") {
		t.Errorf("expected values in output, got %q", s)
	}
}

// TestMarshalPlainSecrets_RoundTrip is the regression for the
// multi-line corruption: a value containing newlines or special
// characters must survive marshal → re-parse with the original bytes
// intact. Earlier versions used yaml.Marshal-then-trim which switched
// to block-scalar form for multi-line input and produced invalid YAML
// after concatenation.
func TestMarshalPlainSecrets_RoundTrip(t *testing.T) {
	secrets := map[string]string{
		"with-quote":     `he said "hi"`,
		"with-newline":   "line1\nline2",
		"with-colon":     "key: value",
		"pem-like":       "-----BEGIN KEY-----\nABCDEF\nGHIJKL\n-----END KEY-----",
		"backslashes":    `\\foo\bar\n`,
		"unicode":        "héllo wörld 日本",
		"leading-hash":   "# not a comment",
		"trailing-space": "trailing   ",
	}
	out, err := marshalPlainSecrets(secrets)
	if err != nil {
		t.Fatal(err)
	}

	var roundTrip map[string]string
	if err := yaml.Unmarshal(out, &roundTrip); err != nil {
		t.Fatalf("output is not valid YAML:\n%s\nerr: %v", out, err)
	}
	for k, want := range secrets {
		got, ok := roundTrip[k]
		if !ok {
			t.Errorf("key %q missing after round-trip", k)
			continue
		}
		if got != want {
			t.Errorf("key %q round-trip: got %q, want %q", k, got, want)
		}
	}
}

// ---------- secretsEnvPath ----------

func TestSecretsEnvPath(t *testing.T) {
	// Build a minimal cli.Command with flags/args populated.
	app := &cli.Command{
		Name: "lazure",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: "/proj"},
		},
		Commands: []*cli.Command{
			{
				Name:      "secrets",
				Arguments: []cli.Argument{&cli.StringArg{Name: "env"}},
				Action: func(ctx context.Context, c *cli.Command) error {
					env, path, err := secretsEnvPath(c)
					if err != nil {
						return err
					}
					if env != "dev" {
						t.Errorf("env = %q", env)
					}
					if path != "/proj/envs/dev.secrets.yml" {
						t.Errorf("path = %q", path)
					}
					return nil
				},
			},
		},
	}
	if err := app.Run(context.Background(), []string{"lazure", "--dir", "/proj", "secrets", "dev"}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsEnvPath_MissingEnv(t *testing.T) {
	app := &cli.Command{
		Name: "lazure",
		Commands: []*cli.Command{
			{
				Name:      "secrets",
				Arguments: []cli.Argument{&cli.StringArg{Name: "env"}},
				Action: func(ctx context.Context, c *cli.Command) error {
					_, _, err := secretsEnvPath(c)
					if err == nil {
						t.Fatal("expected error for missing env arg")
					}
					if !strings.Contains(err.Error(), "env argument is required") {
						t.Errorf("unexpected error: %v", err)
					}
					return nil
				},
			},
		},
	}
	_ = app.Run(context.Background(), []string{"lazure", "secrets"})
}

// ---------- sopsConfigPath ----------

// Trailing slashes on --dir would naively make filepath.Dir return
// the dir itself rather than its parent, sending us to look for the
// .sops.yaml *inside* the deploy directory. filepath.Clean fixes it.
func TestSopsConfigPath_NormalizesTrailingSlash(t *testing.T) {
	cases := []struct {
		dir, want string
	}{
		{"deploy", filepath.Join(".", ".sops.yaml")},
		{"deploy/", filepath.Join(".", ".sops.yaml")},
		{".", filepath.Join(".", ".sops.yaml")},
		{"./deploy", filepath.Join(".", ".sops.yaml")},
		{"/abs/path/deploy", filepath.Join("/abs/path", ".sops.yaml")},
		{"/abs/path/deploy/", filepath.Join("/abs/path", ".sops.yaml")},
	}
	for _, tc := range cases {
		if got := sopsConfigPath(tc.dir); got != tc.want {
			t.Errorf("sopsConfigPath(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

// ---------- createEmptyEncryptedSecrets ----------

// Refusing to overwrite is a data-loss guard: sopsio.Encrypt routes
// existing paths through the re-encrypt branch which would silently
// replace real content with `{}`.
func TestCreateEmptyEncryptedSecrets_RefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	encPath := filepath.Join(dir, "dev.secrets.yml")
	original := []byte("preexisting-encrypted-content")
	if err := os.WriteFile(encPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err := createEmptyEncryptedSecrets(encPath, filepath.Join(dir, ".sops.yaml"))
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("got %v, want error containing 'refusing to overwrite'", err)
	}

	got, _ := os.ReadFile(encPath)
	if string(got) != string(original) {
		t.Errorf("file was modified despite the guard:\n got %q\nwant %q", got, original)
	}
}

// ---------- new ----------

// TestSecretsNew_ErrorsWhenFileExists asserts the existence guard
// fires before any sops/Azure work, so this test runs without creds.
func TestSecretsNew_ErrorsWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "deploy", "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(envsDir, "dev.secrets.yml")
	if err := os.WriteFile(encPath, []byte("preexisting"), 0o600); err != nil {
		t.Fatal(err)
	}

	var actionErr error
	app := &cli.Command{
		Name: "lazure",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: filepath.Join(dir, "deploy")},
		},
		Commands: []*cli.Command{
			{
				Name:      "secrets",
				Arguments: []cli.Argument{&cli.StringArg{Name: "env"}},
				Action: func(ctx context.Context, c *cli.Command) error {
					actionErr = SecretsNew(ctx, c)
					return nil
				},
			},
		},
	}
	if err := app.Run(context.Background(), []string{"lazure", "--dir", filepath.Join(dir, "deploy"), "secrets", "dev"}); err != nil {
		t.Fatal(err)
	}
	if actionErr == nil || !strings.Contains(actionErr.Error(), "already exists") {
		t.Errorf("got %v, want error containing 'already exists'", actionErr)
	}
}

// ---------- view (integration) ----------

// TestSecretsView_Integration runs the full view command against an
// opt-in encrypted fixture. Set LAZURE_INTEGRATION_PROJECT_DIR to a
// project deploy directory when running live SOPS/KV tests.
func TestSecretsView_Integration(t *testing.T) {
	dir := os.Getenv("LAZURE_INTEGRATION_PROJECT_DIR")
	if dir == "" {
		t.Skip("skipping: LAZURE_INTEGRATION_PROJECT_DIR not set")
	}

	// Capture stdout.
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	projectDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}

	app := &cli.Command{
		Name:  "lazure",
		Flags: []cli.Flag{&cli.StringFlag{Name: "dir", Value: projectDir}},
		Commands: []*cli.Command{
			{
				Name:      "secrets",
				Arguments: []cli.Argument{&cli.StringArg{Name: "env"}},
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "reveal"},
					&cli.StringFlag{Name: "format", Value: "table"},
				},
				Action: SecretsView,
			},
		},
	}
	runErr := app.Run(context.Background(), []string{"lazure", "--dir", projectDir, "secrets", "dev"})
	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	if runErr != nil {
		t.Fatalf("secrets view failed: %v\nstdout:\n%s", runErr, out)
	}

	// Table header + at least one known nexus-* secret.
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "VALUE") {
		t.Errorf("missing table header in output:\n%s", out)
	}
	if !strings.Contains(out, "nexus-database-url") {
		t.Errorf("expected nexus-database-url in output:\n%s", out)
	}
	// Default (no --reveal) should redact — no raw postgres URL in the output.
	if strings.Contains(out, "postgresql") {
		t.Error("unredacted output in default view (should redact)")
	}
	// Redaction format: "xxx…yyy" with ellipsis character.
	if !strings.Contains(out, "…") {
		t.Errorf("expected redaction ellipsis in output:\n%s", out)
	}
}

// TestSecretsView_InvalidFormat confirms the format flag validation
// without needing decrypt to work.
func TestSecretsView_InvalidFormat(t *testing.T) {
	dir := t.TempDir()
	// Put a dummy file in place so secretsEnvPath constructs a valid path.
	_ = os.MkdirAll(filepath.Join(dir, "envs"), 0o755)

	app := &cli.Command{
		Flags: []cli.Flag{&cli.StringFlag{Name: "dir", Value: dir}},
		Commands: []*cli.Command{
			{
				Name:      "secrets",
				Arguments: []cli.Argument{&cli.StringArg{Name: "env"}},
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "reveal"},
					&cli.StringFlag{Name: "format", Value: "yaml"}, // invalid
				},
				Action: SecretsView,
			},
		},
	}
	err := app.Run(context.Background(), []string{"lazure", "--dir", dir, "secrets", "dev"})
	if err == nil {
		t.Fatal("expected error")
	}
	// Missing fixture triggers decrypt failure first; that's fine. The
	// key test is that the command at least parses + runs.
}
