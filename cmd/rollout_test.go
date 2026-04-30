package cmd

import (
	"strings"
	"testing"
)

func TestNextSemverTag(t *testing.T) {
	tags := []string{"v0.7.4", "v0.8.1", "not-a-version", "v0.8.0"}

	tests := []struct {
		name string
		bump rolloutBump
		want string
	}{
		{name: "minor default", bump: rolloutBumpMinor, want: "v0.9.0"},
		{name: "patch", bump: rolloutBumpPatch, want: "v0.8.2"},
		{name: "major", bump: rolloutBumpMajor, want: "v1.0.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextSemverTag(tags, tt.bump)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("tag = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNextSemverTag_NoExistingTags(t *testing.T) {
	got, err := nextSemverTag(nil, rolloutBumpMinor)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v0.1.0" {
		t.Fatalf("tag = %q", got)
	}
}

func TestRolloutBumpFromFlags_RejectsMultiple(t *testing.T) {
	_, err := rolloutBumpFromFlags(true, true, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "choose only one") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRolloutCleanTreeError(t *testing.T) {
	err := rolloutCleanTreeError(" M file.go\n?? tmp\n")
	if err == nil {
		t.Fatal("expected dirty tree error")
	}
	if !strings.Contains(err.Error(), "working tree is not clean") || !strings.Contains(err.Error(), "file.go") {
		t.Fatalf("unexpected error: %v", err)
	}
}
