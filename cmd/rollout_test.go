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

func TestRolloutBranchError(t *testing.T) {
	for _, branch := range []string{"main", "master"} {
		if err := rolloutBranchError(branch); err != nil {
			t.Fatalf("branch %q should be allowed, got %v", branch, err)
		}
	}
	for _, branch := range []string{"dev", "feature/x", "HEAD", ""} {
		err := rolloutBranchError(branch)
		if err == nil {
			t.Fatalf("branch %q should be rejected", branch)
		}
		if !strings.Contains(err.Error(), "main or master") {
			t.Fatalf("error should mention main/master, got %v", err)
		}
	}
}
