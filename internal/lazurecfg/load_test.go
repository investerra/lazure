package lazurecfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalManifest is a small-but-valid deploy.yml covering the major shapes
// (app block, ingress, env with both value+secret, a single container) so
// the end-to-end load path exercises more than the toy case.
const minimalManifest = `
app:
  name: "{{ .Vars.app_name }}"
  location: switzerlandnorth
  resource_group: "{{ .Vars.resource_group }}"
  managed_environment_id: /subscriptions/x/resourceGroups/x/providers/Microsoft.App/managedEnvironments/x
  identity: /subscriptions/x/resourceGroups/x/providers/Microsoft.ManagedIdentity/userAssignedIdentities/x

ingress:
  external: true
  target_port: 8000

env:
  APP_ENV: "{{ .Vars.app_env }}"
  LOG_LEVEL: "{{ .Vars.log_level | default "info" }}"
  DATABASE_URL: { secret: nexus-database-url }

containers:
  - name: app
    image: "acr.io/app:{{ .Vars.git_commit }}"
    resources: { cpu: 0.5, memory: 1Gi }
`

func setupLoadProject(t *testing.T, varsContent, manifestContent string) string {
	t.Helper()
	dir := setupProject(t)
	if varsContent != "" {
		if err := os.WriteFile(filepath.Join(dir, "envs", "dev.vars.yml"), []byte(varsContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if manifestContent != "" {
		if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(manifestContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadVars_StdVarsOnly(t *testing.T) {
	dir := setupLoadProject(t, "", "")
	vars, err := LoadVars(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if vars["app_env"] != "dev" {
		t.Errorf("app_env = %v", vars["app_env"])
	}
	if vars["keyvault_url"] != "https://kv-test.vault.azure.net" {
		t.Errorf("keyvault_url = %v", vars["keyvault_url"])
	}
}

func TestLoadVars_UserVarsMergedOverStd(t *testing.T) {
	varsYml := `
app_name: api-server
log_level: debug
app_env: overridden   # intentional attempt to override a std var
`
	dir := setupLoadProject(t, varsYml, "")
	vars, err := LoadVars(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if vars["app_name"] != "api-server" {
		t.Errorf("app_name = %v", vars["app_name"])
	}
	if vars["log_level"] != "debug" {
		t.Errorf("log_level = %v", vars["log_level"])
	}
	// User vars CAN override std vars — documented merge order.
	if vars["app_env"] != "overridden" {
		t.Errorf("app_env = %v, expected user-var override", vars["app_env"])
	}
}

func TestLoadVars_VarsYmlCanReferenceStdVars(t *testing.T) {
	// This is the whole reason vars.yml is itself a template — computed
	// values like docker_image that depend on git_commit.
	varsYml := `
docker_image: "acr.io/app:{{ .Vars.git_commit }}"
`
	dir := setupLoadProject(t, varsYml, "")
	initGitRepo(t, dir)
	commitAll(t, dir, "initial")

	vars, err := LoadVars(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	img := vars["docker_image"].(string)
	if !strings.HasPrefix(img, "acr.io/app:") || len(img) <= len("acr.io/app:") {
		t.Errorf("docker_image = %q, expected 'acr.io/app:<sha>'", img)
	}
}

func TestLoadVars_CLIOverridesWinOverEverything(t *testing.T) {
	varsYml := `app_name: from-file`
	dir := setupLoadProject(t, varsYml, "")
	vars, err := LoadVars(LoadOptions{
		ProjectDir: dir,
		Env:        "dev",
		CLIVars:    map[string]string{"app_name": "from-cli", "extra": "cli-only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if vars["app_name"] != "from-cli" {
		t.Errorf("app_name = %v, want 'from-cli'", vars["app_name"])
	}
	if vars["extra"] != "cli-only" {
		t.Errorf("extra = %v, want 'cli-only'", vars["extra"])
	}
}

func TestLoadVars_MissingVarsFileIsFine(t *testing.T) {
	// Only secrets fixture — no vars.yml. Should load std vars without error.
	dir := setupLoadProject(t, "", "")
	vars, err := LoadVars(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("expected no error for missing vars.yml, got: %v", err)
	}
	if vars["app_env"] != "dev" {
		t.Errorf("vars missing basic std var")
	}
}

func TestLoadVars_MissingKeyIsZeroValue(t *testing.T) {
	// With missingkey=zero (Helm-compatible), an unknown .Vars.X returns
	// the zero value (nil for map[string]any). The `default` function can
	// then substitute. Without `default`, Go's template renders nil as
	// the literal string "<no value>" — users are expected to use either
	// `default` (for optional vars) or `required` (for required ones).
	// Typo detection is the job of `required`, tested separately.
	varsYml := `
without_default: "{{ .Vars.does_not_exist }}"
with_default:    "{{ .Vars.also_missing | default "fallback" }}"
`
	dir := setupLoadProject(t, varsYml, "")
	vars, err := LoadVars(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["without_default"] != "<no value>" {
		t.Errorf("without_default = %q, want '<no value>' (Go template nil rendering)", vars["without_default"])
	}
	if vars["with_default"] != "fallback" {
		t.Errorf("with_default = %q, want 'fallback'", vars["with_default"])
	}
}

func TestLoadManifest_FullRoundTrip(t *testing.T) {
	varsYml := `
app_name: api-server
resource_group: rg-example-dev
`
	dir := setupLoadProject(t, varsYml, minimalManifest)
	initGitRepo(t, dir)
	commitAll(t, dir, "initial")

	m, vars, err := LoadManifest(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.App.Name != "api-server" {
		t.Errorf("Manifest.App.Name = %q, want 'api-server'", m.App.Name)
	}
	if m.App.ResourceGroup != "rg-example-dev" {
		t.Errorf("Manifest.App.ResourceGroup = %q, want 'rg-example-dev'", m.App.ResourceGroup)
	}
	if m.App.Location != "switzerlandnorth" {
		t.Errorf("Manifest.App.Location = %q", m.App.Location)
	}
	if m.Ingress == nil || !m.Ingress.External || m.Ingress.TargetPort != 8000 {
		t.Errorf("Ingress = %+v", m.Ingress)
	}
	if len(m.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(m.Containers))
	}
	c := m.Containers[0]
	if !strings.HasPrefix(c.Image, "acr.io/app:") {
		t.Errorf("container image = %q, expected acr.io/app:<sha>", c.Image)
	}
	// The manifest puts env at the TOP LEVEL (shared). Merging shared into
	// per-container env is task 697.7 — this test verifies parsing only.
	if got := m.Env["APP_ENV"]; got == nil || got.Value != "dev" {
		t.Errorf("shared env APP_ENV = %+v, want {Value: dev}", got)
	}
	if got := m.Env["LOG_LEVEL"]; got == nil || got.Value != "info" {
		t.Errorf("shared env LOG_LEVEL = %+v, want {Value: info} (sprig default)", got)
	}
	if got := m.Env["DATABASE_URL"]; got == nil || got.SecretRef != "nexus-database-url" {
		t.Errorf("shared env DATABASE_URL = %+v, want {SecretRef: nexus-database-url}", got)
	}
	// Per-container env is empty — shared-to-container merge is task 697.7.
	if len(c.Env) != 0 {
		t.Errorf("container env should be empty pre-merge, got %+v", c.Env)
	}
	// git_commit propagated end-to-end
	if git, ok := vars["git_commit"].(string); !ok || len(git) != 40 {
		t.Errorf("git_commit not propagated into vars: %v", vars["git_commit"])
	}
}

func TestRequired_Missing(t *testing.T) {
	manifest := `
app:
  name: "{{ required "name missing" .Vars.app_name }}"
  location: switzerlandnorth
  resource_group: rg
  managed_environment_id: x
  identity: x
containers:
  - name: app
    image: x
`
	dir := setupLoadProject(t, "", manifest)
	_, _, err := LoadManifest(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err == nil {
		t.Fatal("expected error for missing app_name")
	}
	if !strings.Contains(err.Error(), "name missing") {
		t.Errorf("error = %q, want 'name missing' text", err.Error())
	}
}

func TestRequired_Empty(t *testing.T) {
	manifest := `
app:
  name: "{{ required "name missing" .Vars.app_name }}"
  location: x
  resource_group: x
  managed_environment_id: x
  identity: x
containers:
  - name: app
`
	varsYml := `app_name: ""`
	dir := setupLoadProject(t, varsYml, manifest)
	_, _, err := LoadManifest(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err == nil {
		t.Fatal("expected error for empty app_name")
	}
	if !strings.Contains(err.Error(), "name missing") {
		t.Errorf("error = %q, want 'name missing' text", err.Error())
	}
}

func TestRequired_Present(t *testing.T) {
	manifest := `
app:
  name: "{{ required "name missing" .Vars.app_name }}"
  location: x
  resource_group: x
  managed_environment_id: x
  identity: x
containers:
  - name: app
`
	varsYml := `app_name: api-server`
	dir := setupLoadProject(t, varsYml, manifest)
	m, _, err := LoadManifest(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.App.Name != "api-server" {
		t.Errorf("name = %q", m.App.Name)
	}
}

// TestSprigHelpers confirms sprig's default, upper, and trim are available —
// a smoke test that sprig is actually wired, not the full 150-function suite.
func TestSprigHelpers(t *testing.T) {
	manifest := `
app:
  name: "{{ .Vars.app_name | default "fallback" | upper }}"
  location: "{{ " x " | trim }}"
  resource_group: rg
  managed_environment_id: x
  identity: x
containers:
  - name: app
`
	dir := setupLoadProject(t, "", manifest)
	m, _, err := LoadManifest(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.App.Name != "FALLBACK" {
		t.Errorf("name = %q, want 'FALLBACK'", m.App.Name)
	}
	if m.App.Location != "x" {
		t.Errorf("location = %q, want 'x'", m.App.Location)
	}
}

// TestTemplateCannotAccessSecrets verifies there is no .Secrets in the
// rendering context — a design invariant (plaintext secrets never flow
// into the template engine).
func TestTemplateCannotAccessSecrets(t *testing.T) {
	manifest := `
app:
  name: "{{ .Secrets.anything }}"
  location: x
  resource_group: x
  managed_environment_id: x
  identity: x
containers:
  - name: app
`
	dir := setupLoadProject(t, "", manifest)
	_, _, err := LoadManifest(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err == nil {
		t.Fatal("expected error for .Secrets access")
	}
	if !strings.Contains(err.Error(), "Secrets") && !strings.Contains(err.Error(), "can't evaluate") {
		t.Errorf("error = %q, want .Secrets-related error", err.Error())
	}
}
