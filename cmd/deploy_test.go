package cmd

import (
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/lazurecfg"
)

func TestParseCLIVars_Empty(t *testing.T) {
	got, err := parseCLIVars(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected nil/empty, got %+v", got)
	}
}

func TestParseCLIVars_Basic(t *testing.T) {
	got, err := parseCLIVars([]string{"app_name=api-server", "log_level=debug"})
	if err != nil {
		t.Fatal(err)
	}
	if got["app_name"] != "api-server" {
		t.Errorf("app_name = %q", got["app_name"])
	}
	if got["log_level"] != "debug" {
		t.Errorf("log_level = %q", got["log_level"])
	}
}

func TestParseCLIVars_ValueContainsEquals(t *testing.T) {
	// Connection strings + base64 blobs can contain '=' — split on FIRST only.
	got, err := parseCLIVars([]string{"db_url=postgres://foo:bar=baz@host/db"})
	if err != nil {
		t.Fatal(err)
	}
	if got["db_url"] != "postgres://foo:bar=baz@host/db" {
		t.Errorf("db_url split wrong: %q", got["db_url"])
	}
}

func TestParseCLIVars_LastWins(t *testing.T) {
	// Duplicate key: last --var wins (map-write order).
	got, err := parseCLIVars([]string{"k=first", "k=second"})
	if err != nil {
		t.Fatal(err)
	}
	if got["k"] != "second" {
		t.Errorf("k = %q, want 'second'", got["k"])
	}
}

func TestParseCLIVars_Errors(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  string
	}{
		{"no equals", []string{"justakey"}, "want key=value"},
		{"empty key", []string{"=value"}, "empty key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCLIVars(tc.input)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseCLIVars_EmptyValueAllowed(t *testing.T) {
	// "key=" should produce {key: ""} — user might want to explicitly
	// clear a value. Error handling for empty-value happens at the
	// consumer (validate/render), not here.
	got, err := parseCLIVars([]string{"k="})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got["k"]; !ok || v != "" {
		t.Errorf("expected {k: \"\"}, got %+v", got)
	}
}

func TestDeployFlags_IncludesExpectedBoolFlags(t *testing.T) {
	flags := DeployFlags()
	want := []string{"build", "sync", "force", "wait", "logs", "print", "yes"}
	have := map[string]bool{}
	for _, f := range flags {
		if bf, ok := f.(*cli.BoolFlag); ok {
			have[bf.Name] = true
		}
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("DeployFlags() missing --%s", name)
		}
	}
}

// ---------- findAppImage ----------

func TestFindAppImage_PrefersApp(t *testing.T) {
	m := &lazurecfg.Manifest{
		Containers: []lazurecfg.Container{
			{Name: "sidecar", Image: "acr/sidecar:1"},
			{Name: "app", Image: "acr/app:2"},
			{Name: "tasks", Image: "acr/tasks:3"},
		},
	}
	if got := findAppImage(m); got != "acr/app:2" {
		t.Errorf("findAppImage = %q, want 'acr/app:2'", got)
	}
}

func TestFindAppImage_FallsBackToFirst(t *testing.T) {
	m := &lazurecfg.Manifest{
		Containers: []lazurecfg.Container{
			{Name: "worker", Image: "acr/worker:1"},
			{Name: "sidecar", Image: "acr/sidecar:2"},
		},
	}
	if got := findAppImage(m); got != "acr/worker:1" {
		t.Errorf("findAppImage = %q, want 'acr/worker:1'", got)
	}
}

func TestFindAppImage_EmptyContainers(t *testing.T) {
	m := &lazurecfg.Manifest{}
	if got := findAppImage(m); got != "<no containers>" {
		t.Errorf("findAppImage on empty = %q", got)
	}
}

func TestApplyForceRedeployTimestamp_AddsAndReplacesRuntimeEnv(t *testing.T) {
	app := &azurearm.ContainerApp{}
	app.Properties.Template.Containers = []azurearm.Container{
		{Name: "app", Env: []azurearm.EnvVar{{Name: "EXISTING", Value: "1"}}},
		{Name: "worker", Env: []azurearm.EnvVar{{Name: forceRedeployEnvName, Value: "old"}}},
	}
	app.Properties.Template.InitContainers = []azurearm.Container{
		{Name: "init"},
	}

	applyForceRedeployTimestamp(app, "2026-04-28T15:30:00Z")

	if got := envValue(app.Properties.Template.Containers[0].Env, forceRedeployEnvName); got != "2026-04-28T15:30:00Z" {
		t.Errorf("app force env = %q", got)
	}
	if got := envValue(app.Properties.Template.Containers[1].Env, forceRedeployEnvName); got != "2026-04-28T15:30:00Z" {
		t.Errorf("worker force env = %q", got)
	}
	if got := envValue(app.Properties.Template.InitContainers[0].Env, forceRedeployEnvName); got != "" {
		t.Errorf("init force env = %q, want unset", got)
	}
}

func envValue(env []azurearm.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}
