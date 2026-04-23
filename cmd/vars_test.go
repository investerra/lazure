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

	"github.com/urfave/cli/v3"
)

const testVarsSOPSFixture = `nexus-database-url: ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]
sops:
    azure_kv:
        - vault_url: https://kv-test.vault.azure.net
          name: sops
    version: 3.12.2
`

// setupVarsProject creates a minimal project where LoadVars will
// succeed without Azure credentials (sops metadata is plaintext).
func setupVarsProject(t *testing.T, varsContent string) string {
	t.Helper()
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, "dev.secrets.yml"), []byte(testVarsSOPSFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	if varsContent != "" {
		if err := os.WriteFile(filepath.Join(envsDir, "dev.vars.yml"), []byte(varsContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ---------- view ----------

func TestVarsView_Table(t *testing.T) {
	dir := setupVarsProject(t, "app_name: api-server\nlog_level: info\n")

	out, err := runVarsSubcommand(t, dir, "view", []string{"dev"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# vars for dev") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "VALUE") {
		t.Errorf("missing table columns: %q", out)
	}
	if !strings.Contains(out, "app_name") || !strings.Contains(out, "api-server") {
		t.Errorf("missing user var: %q", out)
	}
	// Standard vars should also be present.
	if !strings.Contains(out, "app_env") {
		t.Errorf("missing std var: %q", out)
	}
	if !strings.Contains(out, "keyvault_url") {
		t.Errorf("missing keyvault_url from sops: %q", out)
	}
}

func TestVarsView_JSON(t *testing.T) {
	dir := setupVarsProject(t, "app_name: api-server\n")

	out, err := runVarsSubcommand(t, dir, "view", []string{"dev"}, map[string]string{"format": "json"})
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if parsed["app_name"] != "api-server" {
		t.Errorf("app_name = %v", parsed["app_name"])
	}
	if parsed["keyvault_url"] != "https://kv-test.vault.azure.net" {
		t.Errorf("keyvault_url missing: %v", parsed["keyvault_url"])
	}
}

func TestVarsView_InvalidFormat(t *testing.T) {
	dir := setupVarsProject(t, "")
	_, err := runVarsSubcommand(t, dir, "view", []string{"dev"}, map[string]string{"format": "xml"})
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !strings.Contains(err.Error(), "invalid --format") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestVarsView_MissingEnvArg(t *testing.T) {
	dir := setupVarsProject(t, "")
	_, err := runVarsSubcommand(t, dir, "view", nil, nil)
	if err == nil {
		t.Fatal("expected error for missing env arg")
	}
	if !strings.Contains(err.Error(), "env argument is required") {
		t.Errorf("wrong error: %v", err)
	}
}

// ---------- verify ----------

func TestVarsVerify_Valid(t *testing.T) {
	// Build a valid deploy.yml to pair with the vars.yml.
	dir := setupVarsProject(t, "resource_group: dev-rg\n")
	manifest := `
app:
  name: api-server
  location: switzerlandnorth
  resource_group: "{{ .Vars.resource_group }}"
  managed_environment_id: /subs/x/managedEnvironments/y
  identity: /subs/x/rg/y/identities/z

containers:
  - name: app
    image: acr.io/app:v1
    resources: { cpu: 0.5, memory: 1Gi }
`
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runVarsSubcommand(t, dir, "verify", []string{"dev"}, nil)
	if err != nil {
		t.Fatalf("expected verify to pass: %v", err)
	}
}

func TestVarsVerify_InvalidManifest(t *testing.T) {
	dir := setupVarsProject(t, "")
	// deploy.yml missing required fields (app.location).
	manifest := `
app:
  name: api-server
  resource_group: r
  managed_environment_id: /x
  identity: /x

containers:
  - name: app
    image: a:1
`
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runVarsSubcommand(t, dir, "verify", []string{"dev"}, nil)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "location") {
		t.Errorf("expected location error, got: %v", err)
	}
}

// ---------- helpers ----------

// runVarsSubcommand drives the given vars subcommand through a real
// cli.Command.Run against a temp project, capturing stdout and
// returning the output + any error.
func runVarsSubcommand(t *testing.T, dir, sub string, args []string, flags map[string]string) (string, error) {
	t.Helper()

	// Capture stdout.
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	// Build argv.
	argv := []string{"lazure", "--dir", dir, "vars", sub}
	for k, v := range flags {
		argv = append(argv, "--"+k, v)
	}
	argv = append(argv, args...)

	app := &cli.Command{
		Name: "lazure",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: dir},
		},
		Commands: []*cli.Command{VarsCommand()},
	}

	runErr := app.Run(context.Background(), argv)
	w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), runErr
}
