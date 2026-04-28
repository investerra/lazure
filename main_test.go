package main

import (
	"strings"
	"testing"

	"github.com/investerra/lazure/internal/errs"
)

func TestTopLevelErrorMessage_DoesNotPrintStackTraceInDebug(t *testing.T) {
	err := errs.Wrap(errs.Errorf("async operation Failed: %s", "MANIFEST_UNKNOWN"), "deploy")

	got := topLevelErrorMessage(err, true)
	if got != "deploy: async operation Failed: MANIFEST_UNKNOWN" {
		t.Fatalf("message = %q", got)
	}
	if strings.Contains(got, "\n") || strings.Contains(got, "main.Test") {
		t.Fatalf("message should not include stack frames:\n%s", got)
	}
}
