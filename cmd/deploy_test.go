package cmd

import (
	"strings"
	"testing"

	"github.com/investerra/lazure/internal/lazurecfg"
)

func TestParseCLIVars_Empty(t *testing.T) {
	got, err := parseCLIVars(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil && len(got) != 0 {
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
