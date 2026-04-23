package cmd

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/investerra/lazure/internal/azurearm"
)

// replicaWithContainers builds a replica with the given container names
// in both Containers and InitContainers slots — used only by the
// pickReplica / pickContainer tests. The simpler `replica(name, ready...)`
// helper in restart_test.go stays as-is; names don't collide.
func replicaWithContainers(name string, containers []string, initContainers []string) azurearm.Replica {
	r := azurearm.Replica{Name: name}
	for _, n := range containers {
		r.Properties.Containers = append(r.Properties.Containers, azurearm.ReplicaContainer{
			Name: n, LogStreamEndpoint: "https://logs.example/" + name + "/" + n,
		})
	}
	for _, n := range initContainers {
		r.Properties.InitContainers = append(r.Properties.InitContainers, azurearm.ReplicaContainer{
			Name: n, LogStreamEndpoint: "https://logs.example/" + name + "/" + n,
		})
	}
	return r
}

// ---------- pickReplica ----------

func TestPickReplica_EmptyList(t *testing.T) {
	_, err := pickReplica(nil, "")
	if err == nil || !strings.Contains(err.Error(), "no replicas") {
		t.Errorf("err = %v", err)
	}
}

func TestPickReplica_DefaultsToFirst(t *testing.T) {
	reps := []azurearm.Replica{
		replicaWithContainers("r-1", []string{"app"}, nil),
		replicaWithContainers("r-2", []string{"app"}, nil),
	}
	got, err := pickReplica(reps, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "r-1" {
		t.Errorf("got %q, want r-1", got.Name)
	}
}

func TestPickReplica_ByName(t *testing.T) {
	reps := []azurearm.Replica{
		replicaWithContainers("r-1", []string{"app"}, nil),
		replicaWithContainers("r-2", []string{"app"}, nil),
	}
	got, err := pickReplica(reps, "r-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "r-2" {
		t.Errorf("got %q, want r-2", got.Name)
	}
}

func TestPickReplica_UnknownName(t *testing.T) {
	reps := []azurearm.Replica{replicaWithContainers("r-1", []string{"app"}, nil)}
	_, err := pickReplica(reps, "nope")
	if err == nil || !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "r-1") {
		t.Errorf("err = %v", err)
	}
}

// ---------- pickContainer ----------

func TestPickContainer_DefaultsToFirstRegular(t *testing.T) {
	r := replicaWithContainers("r-1", []string{"app", "tasks"}, []string{"init"})
	got, err := pickContainer(r, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "app" {
		t.Errorf("got %q, want app (regular over init)", got.Name)
	}
}

func TestPickContainer_FallsBackToInitWhenNoRegulars(t *testing.T) {
	r := replicaWithContainers("r-1", nil, []string{"init"})
	got, err := pickContainer(r, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "init" {
		t.Errorf("got %q, want init", got.Name)
	}
}

func TestPickContainer_ByName_Regular(t *testing.T) {
	r := replicaWithContainers("r-1", []string{"app", "tasks"}, []string{"init"})
	got, err := pickContainer(r, "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "tasks" {
		t.Errorf("got %q, want tasks", got.Name)
	}
}

func TestPickContainer_ByName_Init(t *testing.T) {
	r := replicaWithContainers("r-1", []string{"app"}, []string{"init"})
	got, err := pickContainer(r, "init")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "init" {
		t.Errorf("got %q, want init", got.Name)
	}
}

func TestPickContainer_UnknownName(t *testing.T) {
	r := replicaWithContainers("r-1", []string{"app"}, []string{"init"})
	_, err := pickContainer(r, "nope")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") || !strings.Contains(msg, "r-1") ||
		!strings.Contains(msg, "app") || !strings.Contains(msg, "init") {
		t.Errorf("err missing expected parts: %v", err)
	}
}

func TestPickContainer_EmptyReplica(t *testing.T) {
	r := azurearm.Replica{Name: "r-empty"}
	_, err := pickContainer(r, "")
	if err == nil || !strings.Contains(err.Error(), "no containers") {
		t.Errorf("err = %v", err)
	}
}

// ---------- formatLogLine ----------

func TestFormatLogLine_Raw(t *testing.T) {
	const in = `2026-04-23T14:13:55.715Z {"level":"info","message":"hi"}`
	if got := formatLogLine(in, true, false); got != in {
		t.Errorf("raw=true changed line: %q", got)
	}
	if got := formatLogLine(in, true, true); got != in {
		t.Errorf("raw=true + color=true changed line: %q", got)
	}
}

func TestFormatLogLine_JSON_AllFields(t *testing.T) {
	in := `2026-04-23T14:13:55.715Z {"logger":"uvicorn.access","level":"info","timestamp":"2026-04-23T14:13:55.715150Z","message":"GET /health 200"}`
	got := formatLogLine(in, false, false)

	wantParts := []string{
		"2026-04-23T14:13:55.715150Z", // JSON timestamp preferred over ACA prefix
		"INFO",
		"uvicorn.access",
		"GET /health 200",
	}
	for _, w := range wantParts {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	// No ANSI escapes when color=false.
	if strings.Contains(got, "\x1b[") {
		t.Errorf("expected no ANSI escapes, got %q", got)
	}
}

func TestFormatLogLine_JSON_UsesACATimestampWhenJSONHasNone(t *testing.T) {
	in := `2026-04-23T14:13:55.715Z {"level":"info","message":"hi"}`
	got := formatLogLine(in, false, false)
	if !strings.Contains(got, "2026-04-23T14:13:55.715Z") {
		t.Errorf("ACA timestamp not used as fallback: %q", got)
	}
}

func TestFormatLogLine_JSON_MissingLevelAndLogger(t *testing.T) {
	in := `2026-04-23T14:13:55.715Z {"message":"bare message"}`
	got := formatLogLine(in, false, false)
	if !strings.Contains(got, "bare message") {
		t.Errorf("message missing: %q", got)
	}
	// Level column absent — no "INFO " etc.
	for _, lvl := range []string{"INFO", "DEBUG", "WARN", "ERROR"} {
		if strings.Contains(got, lvl) {
			t.Errorf("unexpected level %q in %q", lvl, got)
		}
	}
}

func TestFormatLogLine_JSON_Extras(t *testing.T) {
	in := `2026-04-23T14:13:55.715Z {"message":"req","method":"GET","status":200,"user_id":"u123"}`
	got := formatLogLine(in, false, false)
	// Extras alphabetized: method, status, user_id.
	if !strings.Contains(got, "method=GET status=200 user_id=u123") {
		t.Errorf("extras not rendered as expected: %q", got)
	}
}

func TestFormatLogLine_NotJSON(t *testing.T) {
	const in = `2026-04-23T14:13:55.715Z just a plaintext log line`
	if got := formatLogLine(in, false, false); got != in {
		t.Errorf("non-JSON line changed: %q", got)
	}
}

func TestFormatLogLine_EmptyJSONObject(t *testing.T) {
	const in = `2026-04-23T14:13:55.715Z {}`
	if got := formatLogLine(in, false, false); got != in {
		t.Errorf("empty JSON payload should pass through: %q", got)
	}
}

func TestFormatLogLine_Color_AddsANSI(t *testing.T) {
	// Force lipgloss to emit ANSI even though `go test` stdout isn't a
	// TTY — otherwise termenv auto-detection collapses styles to plain
	// strings and we can't verify color application.
	prev := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.DefaultRenderer().SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.DefaultRenderer().SetColorProfile(prev) })

	in := `2026-04-23T14:13:55.715Z {"level":"error","logger":"x","message":"boom"}`
	got := formatLogLine(in, false, true)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected ANSI escapes when color=true, got %q", got)
	}
	// Structure preserved even with colors.
	for _, want := range []string{"ERROR", "x", "boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in colored output %q", want, got)
		}
	}
}

func TestFormatLogLine_LevelCasing(t *testing.T) {
	// Upper / lower / mixed — all should render a LEVEL column.
	for _, lvl := range []string{"info", "INFO", "Warning", "error"} {
		in := `2026-04-23T14:13:55.715Z {"level":"` + lvl + `","message":"x"}`
		got := formatLogLine(in, false, false)
		want := strings.ToUpper(lvl)
		if !strings.Contains(got, want) {
			t.Errorf("level %q not normalized to upper in output: %q", lvl, got)
		}
	}
}

func TestFormatLogLine_NoACATimestamp(t *testing.T) {
	// Some code paths may strip the ACA prefix upstream; payload is JSON.
	in := `{"level":"info","message":"hi"}`
	got := formatLogLine(in, false, false)
	if !strings.Contains(got, "INFO") || !strings.Contains(got, "hi") {
		t.Errorf("handler didn't parse ACA-prefix-less JSON: %q", got)
	}
}
