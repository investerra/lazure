package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/urfave/cli/v3"
)

// forceLipglossColor pins the lipgloss renderer to TrueColor so ANSI
// escapes appear in the output even though `go test` stdout isn't a
// TTY. Mirrors the pattern in logs_test.go / events_test.go.
func forceLipglossColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.DefaultRenderer().SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.DefaultRenderer().SetColorProfile(prev) })
}

// ---------- parseOnly ----------

func TestParseOnly(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", onlyVarsBit | onlySecretsBit},
		{"all", onlyVarsBit | onlySecretsBit},
		{"vars", onlyVarsBit},
		{"secrets", onlySecretsBit},
		{"bogus", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := parseOnly(tc.in); got != tc.want {
				t.Errorf("parseOnly(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// ---------- formatDotenv ----------

func TestFormatDotenv_PlainNoQuote(t *testing.T) {
	got := formatDotenv("LOG_LEVEL", "info")
	want := `LOG_LEVEL=info`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatDotenv_EmptyValue(t *testing.T) {
	got := formatDotenv("EMPTY", "")
	want := `EMPTY=""`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatDotenv_QuotesWhenNeeded(t *testing.T) {
	cases := []struct{ in, want string }{
		{"with space", `KEY="with space"`},
		{`with"quote`, `KEY="with\"quote"`},
		{`back\slash`, `KEY="back\\slash"`},
		{"line1\nline2", "KEY=\"line1\nline2\""},
		{"$VAR", `KEY="$VAR"`},
		{"# hashy", `KEY="# hashy"`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := formatDotenv("KEY", tc.in); got != tc.want {
				t.Errorf("formatDotenv(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------- printConfigTable / printConfigJSON ----------

func TestPrintConfigTable_RedactsSecretsWithoutReveal(t *testing.T) {
	entries := []resolvedEntry{
		{Key: "DB_HOST", Value: "primary.example.com"},
		{Key: "API_KEY", Value: "super-secret-value", IsSecret: true, SecretRef: "api-key"},
	}
	out := captureStdoutRun(t, func() {
		if err := printConfigTable(os.Stdout, "dev", entries, false, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "DB_HOST") || !strings.Contains(out, "primary.example.com") {
		t.Errorf("plain var missing from output: %q", out)
	}
	if strings.Contains(out, "super-secret-value") {
		t.Errorf("secret value leaked when reveal=false: %q", out)
	}
	if !strings.Contains(out, "secret:api-key") {
		t.Errorf("missing secret: source label: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis redaction marker: %q", out)
	}
}

func TestPrintConfigTable_RevealsSecretValueWhenAsked(t *testing.T) {
	entries := []resolvedEntry{
		{Key: "API_KEY", Value: "super-secret-value", IsSecret: true, SecretRef: "api-key"},
	}
	out := captureStdoutRun(t, func() {
		if err := printConfigTable(os.Stdout, "dev", entries, true, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "super-secret-value") {
		t.Errorf("expected revealed secret value: %q", out)
	}
}

func TestPrintConfigTable_MarksMissingSecret(t *testing.T) {
	entries := []resolvedEntry{
		{Key: "API_KEY", IsSecret: true, SecretRef: "api-key", Missing: true},
	}
	out := captureStdoutRun(t, func() {
		if err := printConfigTable(os.Stdout, "dev", entries, true, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "<missing>") {
		t.Errorf("expected <missing> marker: %q", out)
	}
}

func TestPrintConfigJSON_RichShape(t *testing.T) {
	entries := []resolvedEntry{
		{Key: "DB_HOST", Value: "host"},
		{Key: "API_KEY", Value: "abcdefghij", IsSecret: true, SecretRef: "api-key"},
	}
	out := captureStdoutRun(t, func() {
		if err := printConfigJSON(entries, false); err != nil {
			t.Fatal(err)
		}
	})
	var parsed map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if parsed["DB_HOST"]["source"] != "var" || parsed["DB_HOST"]["value"] != "host" {
		t.Errorf("DB_HOST row wrong: %v", parsed["DB_HOST"])
	}
	if parsed["API_KEY"]["source"] != "secret" || parsed["API_KEY"]["secret_ref"] != "api-key" {
		t.Errorf("API_KEY row wrong: %v", parsed["API_KEY"])
	}
	if parsed["API_KEY"]["value"] == "abcdefghij" {
		t.Errorf("secret value leaked in JSON: %v", parsed["API_KEY"])
	}
}

// ---------- exportValue ----------

func TestExportValue_PlainVar(t *testing.T) {
	got, err := exportValue(resolvedEntry{Key: "K", Value: "v"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v" {
		t.Errorf("got %q, want %q", got, "v")
	}
}

func TestExportValue_SecretMaskedWithoutReveal(t *testing.T) {
	got, err := exportValue(resolvedEntry{Key: "K", IsSecret: true, SecretRef: "x", Value: "real"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "*" {
		t.Errorf("got %q, want %q", got, "*")
	}
}

func TestExportValue_SecretRevealedReturnsValue(t *testing.T) {
	got, err := exportValue(resolvedEntry{Key: "K", IsSecret: true, SecretRef: "x", Value: "real"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "real" {
		t.Errorf("got %q, want %q", got, "real")
	}
}

func TestExportValue_ErrorsOnMissingSecretWhenRevealing(t *testing.T) {
	_, err := exportValue(resolvedEntry{Key: "K", IsSecret: true, SecretRef: "x", Missing: true}, true)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not in SOPS") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExportValue_MissingSecretMaskedWhenHidden(t *testing.T) {
	got, err := exportValue(resolvedEntry{Key: "K", IsSecret: true, SecretRef: "x", Missing: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "*" {
		t.Errorf("got %q, want %q", got, "*")
	}
}

// ---------- sameEntry / formatDiffCell ----------

func TestSameEntry_PlainValuesCompared(t *testing.T) {
	a := resolvedEntry{Key: "K", Value: "x"}
	b := resolvedEntry{Key: "K", Value: "y"}
	if sameEntry(a, b, false) {
		t.Errorf("expected differ for plain values")
	}
	b.Value = "x"
	if !sameEntry(a, b, false) {
		t.Errorf("expected same for identical plain values")
	}
}

func TestSameEntry_SecretsRefOnlyWhenHidden(t *testing.T) {
	a := resolvedEntry{Key: "K", IsSecret: true, SecretRef: "x", Value: "v1"}
	b := resolvedEntry{Key: "K", IsSecret: true, SecretRef: "x", Value: "v2"}
	if !sameEntry(a, b, false) {
		t.Errorf("hidden values must compare same when refs match")
	}
	if sameEntry(a, b, true) {
		t.Errorf("revealed values must compare different when they differ")
	}
}

func TestSameEntry_SecretVsVarAlwaysDiffers(t *testing.T) {
	a := resolvedEntry{Key: "K", Value: "x"}
	b := resolvedEntry{Key: "K", IsSecret: true, SecretRef: "x", Value: "x"}
	if sameEntry(a, b, true) {
		t.Errorf("var and secret must compare different")
	}
}

func TestFormatDiffCell(t *testing.T) {
	if got := formatDiffCell(resolvedEntry{Value: "v"}, false, false); got != "v" {
		t.Errorf("plain: %q", got)
	}
	if got := formatDiffCell(resolvedEntry{IsSecret: true, SecretRef: "r"}, false, false); got != "secret:r" {
		t.Errorf("hidden secret: %q", got)
	}
	if got := formatDiffCell(resolvedEntry{IsSecret: true, SecretRef: "r", Value: "v"}, true, false); got != "v" {
		t.Errorf("revealed secret: %q", got)
	}
	if got := formatDiffCell(resolvedEntry{IsSecret: true, SecretRef: "r", Missing: true}, true, false); got != "<missing>" {
		t.Errorf("missing secret: %q", got)
	}
}

func TestFormatDiffCell_AppliesColorWhenEnabled(t *testing.T) {
	forceLipglossColor(t)
	got := formatDiffCell(resolvedEntry{IsSecret: true, SecretRef: "r"}, false, true)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected ANSI escape in colored output, got %q", got)
	}
	if !strings.Contains(got, "secret:r") {
		t.Errorf("colored cell lost its label: %q", got)
	}
}

// ---------- writeAlignedRows ----------
//
// The whole point of this helper is that ANSI escape codes in a cell
// don't break alignment of subsequent columns — tabwriter does break
// because it counts the escape bytes. This test wraps one cell in the
// real lipgloss style we use and verifies the next column's leading
// position lines up between a colored and an uncolored row.

func TestWriteAlignedRows_AnsiAwareAlignment(t *testing.T) {
	forceLipglossColor(t)
	colored := styleConfigSecret.Render("secret:api-key")
	rows := [][]string{
		{"NAME", "VALUE", "SOURCE"},
		{"DB_HOST", "primary.example.com", "var"},
		{"API_KEY", "abc…xyz", colored},
	}
	var buf bytes.Buffer
	writeAlignedRows(&buf, rows)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), buf.String())
	}
	// Strip ANSI before measuring; compare *visible* (rune-aware,
	// ANSI-aware) column positions so multibyte runes like `…` and
	// styled cells line up the way a user sees them.
	visiblePos := func(line, target string) int {
		stripped := ansiStrip(line)
		before, _, ok := strings.Cut(stripped, target)
		if !ok {
			t.Fatalf("target %q not found in %q", target, stripped)
		}
		return lipgloss.Width(before)
	}
	if h, d := visiblePos(lines[0], "VALUE"), visiblePos(lines[1], "primary.example.com"); h != d {
		t.Errorf("VALUE column misaligned: header=%d, row=%d", h, d)
	}
	if a, b := visiblePos(lines[1], "var"), visiblePos(lines[2], "secret:api-key"); a != b {
		t.Errorf("SOURCE column misaligned across colored/plain rows: %d vs %d\n%s",
			a, b, buf.String())
	}
}

// ansiStrip removes ANSI escape sequences so tests can assert on the
// visible string layout. Matches the standard CSI form `\x1b[...m`.
func ansiStrip(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ---------- end-to-end (vars-only path) ----------
//
// Tests routed through the real CLI. We use --only=vars so the SOPS
// decrypt path isn't exercised — that lets us run without age keys
// while still covering the full LoadManifest → resolveContainerEnv →
// print pipeline.

func TestConfigKeys_VarsOnly_EndToEnd(t *testing.T) {
	dir := setupConfigProject(t)
	out, err := runConfigSubcommand(t, dir, "keys", []string{"dev"}, map[string]string{"only": "vars"})
	if err != nil {
		t.Fatalf("run failed: %v\nstdout:\n%s", err, out)
	}
	if !strings.Contains(out, "LOG_LEVEL") {
		t.Errorf("expected LOG_LEVEL in keys output:\n%s", out)
	}
	if strings.Contains(out, "API_KEY") {
		t.Errorf("API_KEY (secret) should be filtered out by --only=vars:\n%s", out)
	}
}

func TestConfigView_VarsOnly_EndToEnd(t *testing.T) {
	dir := setupConfigProject(t)
	out, err := runConfigSubcommand(t, dir, "view", []string{"dev"}, map[string]string{"only": "vars"})
	if err != nil {
		t.Fatalf("run failed: %v\nstdout:\n%s", err, out)
	}
	if !strings.Contains(out, "# config for dev") {
		t.Errorf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "LOG_LEVEL") || !strings.Contains(out, "info") {
		t.Errorf("missing var row:\n%s", out)
	}
}

func TestConfigExport_VarsOnly_EndToEnd(t *testing.T) {
	dir := setupConfigProject(t)
	out, err := runConfigSubcommand(t, dir, "export", []string{"dev"}, map[string]string{"only": "vars"})
	if err != nil {
		t.Fatalf("run failed: %v\nstdout:\n%s", err, out)
	}
	if !strings.Contains(out, "export LOG_LEVEL='info'") {
		t.Errorf("missing export line:\n%s", out)
	}
}

func TestConfigGet_PlainVar_EndToEnd(t *testing.T) {
	dir := setupConfigProject(t)
	out, err := runConfigSubcommand(t, dir, "get", []string{"dev", "LOG_LEVEL"}, nil)
	if err != nil {
		t.Fatalf("run failed: %v\nstdout:\n%s", err, out)
	}
	if strings.TrimSpace(out) != "info" {
		t.Errorf("got %q, want %q", strings.TrimSpace(out), "info")
	}
}

func TestConfigGet_MissingKey_Errors(t *testing.T) {
	dir := setupConfigProject(t)
	_, err := runConfigSubcommand(t, dir, "get", []string{"dev", "DOES_NOT_EXIST"}, nil)
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------- verify ----------
//
// Full SOPS decrypt is opt-in (LAZURE_INTEGRATION_PROJECT_DIR), same
// pattern as TestSecretsView_Integration. The unit tests below cover
// the early-return paths that don't need a real age key:
//
//   - missing env arg
//   - structural validation failure (short-circuits before decrypt)

func TestConfigVerify_MissingEnv(t *testing.T) {
	dir := setupConfigProject(t)
	_, err := runConfigSubcommand(t, dir, "verify", nil, nil)
	if err == nil {
		t.Fatal("expected error for missing env arg")
	}
	if !strings.Contains(err.Error(), "env argument is required") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestConfigVerify_InvalidManifest_ShortCircuitsBeforeDecrypt(t *testing.T) {
	dir := setupConfigProject(t)
	// Overwrite the manifest with one missing the required app.location
	// field. verify.Vars must surface the structural error and exit
	// before reaching the SOPS decrypt step (which would fail on the
	// fake-encrypted fixture and mask the real diagnostic).
	bad := `
app:
  name: api-server
  resource_group: dev-rg
  managed_environment_id: /subs/x/managedEnvironments/y
  identity: /subs/x/rg/y/identities/z

containers:
  - name: app
    image: acr.io/app:v1
    resources: { cpu: 0.5, memory: 1Gi }
`
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runConfigSubcommand(t, dir, "verify", []string{"dev"}, nil)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "location") {
		t.Errorf("expected structural error about location, got: %v", err)
	}
	// Make sure we didn't fall through into the decrypt path — the
	// fake fixture would return a SOPS-specific message and that
	// would mean the short-circuit didn't take.
	if strings.Contains(err.Error(), "decrypt secrets") {
		t.Errorf("decrypt ran despite structural failure (no short-circuit): %v", err)
	}
}

// ---------- helpers ----------

const configTestSOPSFixture = `nexus-database-url: ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]
sops:
    azure_kv:
        - vault_url: https://kv-test.vault.azure.net
          name: sops
    version: 3.12.2
`

const configTestManifest = `
app:
  name: api-server
  location: switzerlandnorth
  resource_group: dev-rg
  managed_environment_id: /subs/x/managedEnvironments/y
  identity: /subs/x/rg/y/identities/z

env:
  LOG_LEVEL: "{{ .Vars.log_level }}"
  API_KEY: { secret: nexus-database-url }

containers:
  - name: app
    image: acr.io/app:v1
    resources: { cpu: 0.5, memory: 1Gi }
`

// setupConfigProject builds a minimal project with a manifest that has
// one plain env var and one secret ref. The SOPS metadata is parseable
// so LoadManifest succeeds without keys; we only exercise paths that
// don't decrypt.
func setupConfigProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, "dev.secrets.yml"), []byte(configTestSOPSFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, "dev.vars.yml"), []byte("log_level: info\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(configTestManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runConfigSubcommand drives ConfigCommand() through a real cli.Command.Run
// so the full flag/arg machinery is exercised. Captures stdout.
func runConfigSubcommand(t *testing.T, dir, sub string, args []string, flags map[string]string) (string, error) {
	t.Helper()

	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	argv := []string{"lazure", "--dir", dir, "config", sub}
	for k, v := range flags {
		argv = append(argv, "--"+k, v)
	}
	argv = append(argv, args...)

	app := &cli.Command{
		Name: "lazure",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: dir},
		},
		Commands: []*cli.Command{ConfigCommand()},
	}
	runErr := app.Run(context.Background(), argv)
	w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), runErr
}
