package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/investerra/lazure/internal/azureapi"
)

func TestContainerAppSystemLogsQuery(t *testing.T) {
	since := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)

	t.Run("app only", func(t *testing.T) {
		q := containerAppSystemLogsQuery("myapp", "", "", since)
		for _, want := range []string{
			"ContainerAppSystemLogs_CL",
			`ContainerAppName_s == 'myapp'`,
			"TimeGenerated > datetime(2026-07-09T10:00:00Z)",
			"order by TimeGenerated asc",
		} {
			if !strings.Contains(q, want) {
				t.Errorf("query missing %q:\n%s", want, q)
			}
		}
		if strings.Contains(q, "RevisionName_s") || strings.Contains(q, "ReplicaName_s") {
			t.Errorf("query should not filter revision/replica when unset:\n%s", q)
		}
	})

	t.Run("with revision and replica", func(t *testing.T) {
		q := containerAppSystemLogsQuery("myapp", "myapp--rev2", "myapp--rev2-abcde", since)
		for _, want := range []string{
			`RevisionName_s == 'myapp--rev2'`,
			`ReplicaName_s == 'myapp--rev2-abcde'`,
		} {
			if !strings.Contains(q, want) {
				t.Errorf("query missing %q:\n%s", want, q)
			}
		}
	})
}

func TestKqlQuote(t *testing.T) {
	if got := kqlQuote(`o'brien`); got != `o\'brien` {
		t.Errorf("kqlQuote = %q", got)
	}
}

func TestParseSystemLogTimestamp(t *testing.T) {
	row := azureapi.LogAnalyticsRow{"TimeGenerated": "2026-07-09T10:00:05.0000000Z"}
	ts, ok := parseSystemLogTimestamp(row)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ts.Hour() != 10 || ts.Minute() != 0 || ts.Second() != 5 {
		t.Errorf("ts = %v", ts)
	}

	if _, ok := parseSystemLogTimestamp(azureapi.LogAnalyticsRow{}); ok {
		t.Error("expected ok=false for missing field")
	}
	if _, ok := parseSystemLogTimestamp(azureapi.LogAnalyticsRow{"TimeGenerated": "not-a-time"}); ok {
		t.Error("expected ok=false for unparseable timestamp")
	}
}

func TestFormatSystemLogRow(t *testing.T) {
	row := azureapi.LogAnalyticsRow{
		"TimeGenerated": "2026-07-09T10:00:05.0000000Z",
		"Type_s":        "Warning",
		"Reason_s":      "BackOff",
		"ReplicaName_s": "myapp--rev2-abcde",
		"Log_s":         "Back-off restarting failed container",
	}
	got := formatSystemLogRow(row, false, false)
	for _, want := range []string{
		"2026-07-09T10:00:05.0000000Z",
		"WARNING",
		"BackOff:",
		"[myapp--rev2-abcde]",
		"Back-off restarting failed container",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted row missing %q:\n%s", want, got)
		}
	}
}

func TestFormatSystemLogRow_Raw(t *testing.T) {
	row := azureapi.LogAnalyticsRow{"Log_s": "hello"}
	got := formatSystemLogRow(row, true, false)
	if !strings.Contains(got, `"Log_s":"hello"`) {
		t.Errorf("raw output = %q", got)
	}
}

func TestFormatSystemLogRow_UnrecognizedSchemaFallsBackToJSON(t *testing.T) {
	row := azureapi.LogAnalyticsRow{"SomeOtherColumn_s": "value"}
	got := formatSystemLogRow(row, false, false)
	if !strings.Contains(got, "SomeOtherColumn_s") {
		t.Errorf("expected fallback JSON dump, got %q", got)
	}
}
