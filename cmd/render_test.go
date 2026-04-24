package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"
)

const testManifest = `
app:
  name: api-server
  location: switzerlandnorth
  resource_group: "{{ .Vars.resource_group }}"
  managed_environment_id: /subs/x/managedEnvironments/env
  identity: /subs/x/rg/y/identities/api-server

ingress:
  external: true
  target_port: 8000

env:
  APP_ENV: "{{ .Vars.app_env }}"
  DATABASE_URL: { secret: nexus-database-url }

containers:
  - name: app
    image: "acr.io/app:{{ .Vars.git_commit }}"
    resources: { cpu: 0.5, memory: 1Gi }
`

const testVars = `
resource_group: rg-example-dev
`

const testSecretsFixture = `nexus-database-url: ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]
sops:
    azure_kv:
        - vault_url: https://kv-test.vault.azure.net
          name: sops
          version: 0
    version: 3.12.2
`

func setupTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(testManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, "dev.vars.yml"), []byte(testVars), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, "dev.secrets.yml"), []byte(testSecretsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	// Minimal git repo so stdvars can populate git_commit.
	runGit(t, dir, "init", "-q", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// runRender invokes the full CLI path for `lazure render <env>` against a
// temp project and captures stdout.
func runRender(t *testing.T, dir, env string) string {
	t.Helper()

	// Redirect stdout.
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	// Build a minimal CLI command just for the render action. We don't go
	// through main.go because that would re-parse os.Args; here we directly
	// construct a command with the right flags/args populated.
	app := &cli.Command{
		Name: "lazure",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: dir},
		},
		Commands: []*cli.Command{
			{
				Name: "render",
				Arguments: []cli.Argument{
					&cli.StringArg{Name: "env"},
				},
				Action: Render,
			},
		},
	}
	runErr := app.Run(context.Background(), []string{"lazure", "--dir", dir, "render", env})

	// Restore stdout + collect.
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if runErr != nil {
		t.Fatalf("render error: %v\ncaptured stdout:\n%s", runErr, buf.String())
	}
	return buf.String()
}

func TestRender_ValidManifest_ProducesValidARMYaml(t *testing.T) {
	dir := setupTestProject(t)
	out := runRender(t, dir, "dev")

	if !strings.Contains(out, "type: Microsoft.App/containerApps") {
		t.Errorf("missing ARM type declaration. output:\n%s", out)
	}
	if !strings.Contains(out, "name: api-server") {
		t.Error("missing app name")
	}
	if !strings.Contains(out, "location: switzerlandnorth") {
		t.Error("missing location")
	}
	// ARM uses the wrapper struct; resource_group lives in the request URL,
	// not the body. Skip the assertion and check image instead.
	if !strings.Contains(out, "acr.io/app:") {
		t.Errorf("image template not rendered. output:\n%s", out)
	}

	// Secret ref got auto-stanza'd with the vault URL from SOPS.
	if !strings.Contains(out, "nexus-database-url") {
		t.Error("expected secret ref in output")
	}
	if !strings.Contains(out, "https://kv-test.vault.azure.net/secrets/nexus-database-url") {
		t.Error("expected full KV URL in secrets stanza")
	}

	// Output must be valid YAML that parses back.
	var check map[string]any
	if err := yaml.Unmarshal([]byte(out), &check); err != nil {
		t.Errorf("output is not valid YAML: %v", err)
	}
}

func TestRender_MissingEnvFile_Errors(t *testing.T) {
	dir := t.TempDir()
	// Create an empty project — no deploy.yml, no envs/
	app := &cli.Command{
		Name: "lazure",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: dir},
		},
		Commands: []*cli.Command{
			{Name: "render", Arguments: []cli.Argument{&cli.StringArg{Name: "env"}}, Action: Render},
		},
	}
	err := app.Run(context.Background(), []string{"lazure", "--dir", dir, "render", "dev"})
	if err == nil {
		t.Fatal("expected error for missing project files")
	}
	if !strings.Contains(err.Error(), "render") {
		t.Errorf("error not wrapped with 'render': %v", err)
	}
}

func TestRender_MissingEnvArg_Errors(t *testing.T) {
	app := &cli.Command{
		Name: "lazure",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: "."},
		},
		Commands: []*cli.Command{
			{Name: "render", Arguments: []cli.Argument{&cli.StringArg{Name: "env"}}, Action: Render},
		},
	}
	err := app.Run(context.Background(), []string{"lazure", "render"})
	if err == nil {
		t.Fatal("expected error for missing env arg")
	}
	if !strings.Contains(err.Error(), "env argument is required") {
		t.Errorf("wrong error: %v", err)
	}
}
