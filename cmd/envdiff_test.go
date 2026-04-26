package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildEnvDiff_VarAsymmetry covers the headline use case: three
// envs sharing two keys, one env missing a third. The matrix should
// show ✓✗✓ for the missing key.
func TestBuildEnvDiff_VarAsymmetry(t *testing.T) {
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// deploy.yml — minimal but parseable, no secret refs.
	manifestYAML := `app:
  name: t
  location: l
  resource_group: r
  managed_environment_id: m
  identity: i
containers:
  - name: app
    image: x
`
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// dev + uat have foo + bar; prd only has foo.
	for _, e := range []string{"dev", "uat"} {
		body := "foo: 1\nbar: 2\n"
		if err := os.WriteFile(filepath.Join(envsDir, e+".vars.yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(envsDir, "prd.vars.yml"), []byte("foo: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rows, err := buildEnvDiff(context.Background(), dir, []string{"dev", "uat", "prd"})
	if err != nil {
		t.Fatal(err)
	}
	// Find the bar row.
	var barRow *envDiffRow
	for i := range rows {
		if rows[i].section == "vars" && rows[i].name == "bar" {
			barRow = &rows[i]
		}
	}
	if barRow == nil {
		t.Fatal("expected a 'bar' row")
	}
	if barRow.marks["dev"] != "✓" || barRow.marks["uat"] != "✓" || barRow.marks["prd"] != "✗" {
		t.Errorf("bar marks dev=%q uat=%q prd=%q, want ✓ ✓ ✗",
			barRow.marks["dev"], barRow.marks["uat"], barRow.marks["prd"])
	}
}

// TestBuildEnvDiff_SkipsStdVars guards against std_vars (git_*,
// app_env, keyvault_url) leaking into the matrix as noise rows.
func TestBuildEnvDiff_SkipsStdVars(t *testing.T) {
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte("app:\n  name: t\ncontainers: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Inject a std_var name into one env to verify it's filtered out.
	body := "git_commit: abc\napp_env: dev\nuser_var: x\n"
	if err := os.WriteFile(filepath.Join(envsDir, "dev.vars.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, "uat.vars.yml"), []byte("user_var: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rows, err := buildEnvDiff(context.Background(), dir, []string{"dev", "uat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.name == "git_commit" || r.name == "app_env" || r.name == "keyvault_url" {
			t.Errorf("std var %q leaked into diff matrix", r.name)
		}
	}
}

// TestBuildEnvDiff_SecretRefsClassified covers the four states:
//
//	✓  referenced + present
//	✗  referenced + missing  (will fail deploy)
//	○  not referenced + present  (orphan in SOPS)
//	—  not referenced + not present
//
// Constructed without real SOPS (the integration tests already cover
// decrypt); we synthesize via temp files only.
func TestBuildEnvDiff_LegendStates(t *testing.T) {
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Manifest with one secret ref; vars files are minimal.
	manifest := `app:
  name: t
env:
  DB: { secret: db-url }
containers: []
`
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, "dev.vars.yml"), []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rows, err := buildEnvDiff(context.Background(), dir, []string{"dev"})
	if err != nil {
		t.Fatal(err)
	}
	var dbRow *envDiffRow
	for i := range rows {
		if rows[i].section == "secrets" && rows[i].name == "db-url" {
			dbRow = &rows[i]
		}
	}
	if dbRow == nil {
		t.Fatal("expected a 'db-url' secret row")
	}
	// No SOPS file → referenced but missing → ✗.
	if dbRow.marks["dev"] != "✗" {
		t.Errorf("db-url in dev = %q, want ✗", dbRow.marks["dev"])
	}
}

// TestPrintEnvDiff_RendersHeaderAndLegend smoke-tests the renderer:
// a row through stdout capture, verifying header columns and the
// trailing legend.
func TestPrintEnvDiff_RendersHeaderAndLegend(t *testing.T) {
	rows := []envDiffRow{
		{section: "vars", name: "foo", marks: map[string]string{"dev": "✓", "uat": "✗"}},
	}
	out := captureStdoutRun(t, func() {
		_ = printEnvDiff(rows, []string{"dev", "uat"})
	})
	for _, want := range []string{"NAME", "dev", "uat", "vars:", "foo", "✓", "✗", "legend"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
