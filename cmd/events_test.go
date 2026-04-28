package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/investerra/lazure/internal/azureapi"
)

func TestPrintEventsTable_ExpandShowsNestedStatusMessageError(t *testing.T) {
	event := azureapi.ActivityEvent{
		EventTimestamp: time.Date(2026, 4, 28, 10, 25, 59, 0, time.UTC),
		Caller:         "user@example.com",
		ResourceID:     "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/containerApps/kyc",
		CorrelationID:  "corr-1",
		OperationID:    "op-1",
		Level:          "Error",
		Properties: azureapi.ActivityEventProperties{
			StatusMessage: `{"status":"Failed","error":{"code":"ResourceOperationFailure","message":"The resource operation completed with terminal provisioning state 'Failed'.","details":[{"code":"ContainerAppOperationError","message":"Failed to provision revision for container app 'kyc'. Error details: image pull UNAUTHORIZED"}]}}`,
		},
	}
	event.OperationName.LocalizedValue = "Create or Update Container App"
	event.Status.LocalizedValue = "Failed"

	out, err := captureStdout(t, func() error {
		return printEventsTable([]azureapi.ActivityEvent{event}, "dev", true, false)
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`activity log for env "dev"`,
		"Create or Update Container App",
		"correlation: corr-1",
		"operation:   op-1",
		"level:       Error",
		"error:       ResourceOperationFailure",
		"message:     The resource operation completed with terminal provisioning state 'Failed'.",
		"detail:      ContainerAppOperationError: Failed to provision revision for container app 'kyc'. Error details: image pull UNAUTHORIZED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded events output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintEventsTable_ExpandFallsBackToRawStatusMessage(t *testing.T) {
	event := azureapi.ActivityEvent{
		EventTimestamp: time.Date(2026, 4, 28, 10, 25, 59, 0, time.UTC),
		Caller:         "user@example.com",
		CorrelationID:  "corr-raw",
		Properties: azureapi.ActivityEventProperties{
			StatusMessage: "plain azure failure text",
		},
	}
	event.OperationName.Value = "Microsoft.App/containerApps/write"
	event.Status.Value = "Failed"

	out, err := captureStdout(t, func() error {
		return printEventsTable([]azureapi.ActivityEvent{event}, "dev", true, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "statusMessage: plain azure failure text") {
		t.Errorf("expanded events output should include raw statusMessage:\n%s", out)
	}
}

func TestPrintEventsTable_ExpandSeparatesRecords(t *testing.T) {
	first := azureapi.ActivityEvent{
		EventTimestamp: time.Date(2026, 4, 28, 10, 25, 59, 0, time.UTC),
		Caller:         "user@example.com",
		CorrelationID:  "corr-1",
	}
	first.OperationName.Value = "op-1"
	first.Status.Value = "Failed"

	second := azureapi.ActivityEvent{
		EventTimestamp: time.Date(2026, 4, 28, 10, 24, 59, 0, time.UTC),
		Caller:         "user@example.com",
		CorrelationID:  "corr-2",
	}
	second.OperationName.Value = "op-2"
	second.Status.Value = "Started"

	out, err := captureStdout(t, func() error {
		return printEventsTable([]azureapi.ActivityEvent{first, second}, "dev", true, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "corr-1\n\n2026-04-28") {
		t.Errorf("expanded records should be separated by a blank line:\n%s", out)
	}
}

func TestPrintEventsTable_ExpandColorsStatusAndErrorValues(t *testing.T) {
	prev := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.DefaultRenderer().SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.DefaultRenderer().SetColorProfile(prev) })

	event := azureapi.ActivityEvent{
		EventTimestamp: time.Date(2026, 4, 28, 10, 25, 59, 0, time.UTC),
		Level:          "Error",
		Properties: azureapi.ActivityEventProperties{
			StatusMessage: `{"status":"Failed","error":{"code":"ResourceOperationFailure","message":"boom"}}`,
		},
	}
	event.OperationName.Value = "Microsoft.App/containerApps/write"
	event.Status.Value = "Failed"

	out, err := captureStdout(t, func() error {
		return printEventsTable([]azureapi.ActivityEvent{event}, "dev", true, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("expected ANSI color escapes in expanded events output:\n%s", out)
	}
	if !strings.Contains(out, "Failed") || !strings.Contains(out, "ResourceOperationFailure") {
		t.Errorf("coloring should preserve readable text:\n%s", out)
	}
}
