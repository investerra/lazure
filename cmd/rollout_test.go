package cmd

import (
	"strings"
	"testing"
)

func TestRolloutCleanTreeError(t *testing.T) {
	err := rolloutCleanTreeError(" M file.go\n?? tmp\n")
	if err == nil {
		t.Fatal("expected dirty tree error")
	}
	if !strings.Contains(err.Error(), "working tree is not clean") || !strings.Contains(err.Error(), "file.go") {
		t.Fatalf("unexpected error: %v", err)
	}
}
