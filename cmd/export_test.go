package cmd

import (
	"testing"

	"github.com/investerra/lazure/internal/lazurecfg"
)

func TestFormatExport_PlainValue(t *testing.T) {
	got := formatExport("LOG_LEVEL", "info")
	want := `export LOG_LEVEL='info'`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatExport_EmptyValue(t *testing.T) {
	got := formatExport("EMPTY", "")
	want := `export EMPTY=''`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatExport_EscapesSingleQuotes(t *testing.T) {
	// 'it's' becomes 'it'\''s' — close, escape, reopen.
	got := formatExport("MSG", "it's a test")
	want := `export MSG='it'\''s a test'`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatExport_NewlineInValue(t *testing.T) {
	// Single-quoted strings preserve literal newlines in POSIX shells,
	// so multi-line values just round-trip through `eval`.
	got := formatExport("PEM", "line1\nline2")
	want := "export PEM='line1\nline2'"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPickManifestContainer_DefaultPicksFirstRegular(t *testing.T) {
	m := &lazurecfg.Manifest{
		InitContainers: []lazurecfg.Container{{Name: "init"}},
		Containers:     []lazurecfg.Container{{Name: "app"}, {Name: "tasks"}},
	}
	got, err := pickManifestContainer(m, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "app" {
		t.Errorf("default = %q, want app", got.Name)
	}
}

func TestPickManifestContainer_DefaultFallsBackToInit(t *testing.T) {
	m := &lazurecfg.Manifest{
		InitContainers: []lazurecfg.Container{{Name: "init"}},
	}
	got, err := pickManifestContainer(m, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "init" {
		t.Errorf("default = %q, want init", got.Name)
	}
}

func TestPickManifestContainer_NamedMatch(t *testing.T) {
	m := &lazurecfg.Manifest{
		Containers: []lazurecfg.Container{{Name: "app"}, {Name: "tasks"}},
	}
	got, err := pickManifestContainer(m, "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "tasks" {
		t.Errorf("got %q, want tasks", got.Name)
	}
}

func TestPickManifestContainer_NamedNotFound(t *testing.T) {
	m := &lazurecfg.Manifest{
		Containers: []lazurecfg.Container{{Name: "app"}},
	}
	_, err := pickManifestContainer(m, "missing")
	if err == nil {
		t.Fatal("expected error for unknown container name")
	}
}

func TestPickManifestContainer_NoContainers(t *testing.T) {
	_, err := pickManifestContainer(&lazurecfg.Manifest{}, "")
	if err == nil {
		t.Fatal("expected error for empty manifest")
	}
}
