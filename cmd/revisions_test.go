package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/investerra/lazure/internal/azurearm"
)

func TestHumanAge(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Second, "10s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{7 * 24 * time.Hour, "7d"},
		{30 * 24 * time.Hour, "30d"},
	}
	for _, tc := range cases {
		if got := humanAge(tc.in); got != tc.want {
			t.Errorf("humanAge(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFindLatest(t *testing.T) {
	revs := []azurearm.Revision{
		{Name: "app--a", Properties: azurearm.RevisionProperties{TrafficWeight: 20}},
		{Name: "app--b", Properties: azurearm.RevisionProperties{TrafficWeight: 80}},
		{Name: "app--c", Properties: azurearm.RevisionProperties{TrafficWeight: 0}},
	}
	if got := findLatest(revs); got != "app--b" {
		t.Errorf("findLatest = %q, want app--b (highest traffic)", got)
	}
}

func TestFindLatest_AllZero(t *testing.T) {
	// All revisions inactive (e.g. Single mode where only one is active
	// and the rest have 0 weight). The first one with 0 still gets
	// picked because -1 is the initial best — this is a bit loose but
	// matches what the user sees: "the first revision" is typically the
	// one just deployed.
	revs := []azurearm.Revision{
		{Name: "app--a", Properties: azurearm.RevisionProperties{TrafficWeight: 0}},
		{Name: "app--b", Properties: azurearm.RevisionProperties{TrafficWeight: 0}},
	}
	if got := findLatest(revs); got != "app--a" {
		t.Errorf("findLatest on all-zero = %q, want app--a", got)
	}
}

func TestPrintRevisionsTable(t *testing.T) {
	now := time.Now()
	revs := []azurearm.Revision{
		{
			Name: "app--latest",
			Properties: azurearm.RevisionProperties{
				CreatedTime:   now.Add(-2 * time.Hour),
				Active:        true,
				Replicas:      3,
				TrafficWeight: 100,
				RunningState:  "Running",
				HealthState:   "Healthy",
			},
		},
		{
			Name: "app--old",
			Properties: azurearm.RevisionProperties{
				CreatedTime:   now.Add(-3 * 24 * time.Hour),
				Active:        false,
				Replicas:      0,
				TrafficWeight: 0,
				RunningState:  "Inactive",
				HealthState:   "",
			},
		},
	}
	out, err := captureStdout(t, func() error { return printRevisionsTable("dev", "app", revs) })
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"# revisions for app / dev",
		"NAME", "AGE", "TRAFFIC", "REPLICAS", "STATE", "HEALTH",
		"app--latest (latest)",
		"100%",
		"Running", "Healthy",
		"app--old",
		"Inactive",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Health fallback for empty HealthState.
	if !strings.Contains(out, "- ") {
		// At least one '-' for the blank health column.
		t.Errorf("expected '-' fallback for blank health: %s", out)
	}
}

func TestPrintRevisionsJSON(t *testing.T) {
	revs := []azurearm.Revision{
		{Name: "app--abc", Properties: azurearm.RevisionProperties{TrafficWeight: 100}},
	}
	out, err := captureStdout(t, func() error { return printRevisionsJSON(revs) })
	if err != nil {
		t.Fatal(err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if len(parsed) != 1 || parsed[0]["name"] != "app--abc" {
		t.Errorf("JSON shape unexpected: %+v", parsed)
	}
}

func TestStringOr(t *testing.T) {
	if got := stringOr("hello", "fallback"); got != "hello" {
		t.Errorf("stringOr nonempty = %q, want 'hello'", got)
	}
	if got := stringOr("", "fallback"); got != "fallback" {
		t.Errorf("stringOr empty = %q, want 'fallback'", got)
	}
}
