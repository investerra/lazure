package cmd

import (
	"testing"

	"github.com/investerra/lazure/internal/azurearm"
)

func revs(names ...string) []azurearm.Revision {
	out := make([]azurearm.Revision, len(names))
	for i, n := range names {
		out[i] = azurearm.Revision{Name: n}
	}
	return out
}

func TestFilterRollbackCandidates_ExcludesCurrent(t *testing.T) {
	all := revs("a", "b", "c", "d", "e")
	got := filterRollbackCandidates(all, "b", 5)
	gotNames := make([]string, len(got))
	for i, r := range got {
		gotNames[i] = r.Name
	}
	want := []string{"a", "c", "d", "e"}
	if len(gotNames) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(gotNames), len(want), gotNames)
	}
	for i, n := range want {
		if gotNames[i] != n {
			t.Errorf("got[%d] = %q, want %q", i, gotNames[i], n)
		}
	}
}

func TestFilterRollbackCandidates_CapsAtMax(t *testing.T) {
	all := revs("a", "b", "c", "d", "e", "f", "g", "h")
	got := filterRollbackCandidates(all, "a", 3)
	if len(got) != 3 {
		t.Errorf("len = %d, want 3 (capped)", len(got))
	}
	// Should be b, c, d — first three after excluding a.
	if got[0].Name != "b" || got[1].Name != "c" || got[2].Name != "d" {
		names := []string{got[0].Name, got[1].Name, got[2].Name}
		t.Errorf("got = %v, want [b c d]", names)
	}
}

func TestFilterRollbackCandidates_PreservesOrder(t *testing.T) {
	// Azure returns newest-first; filter shouldn't reorder.
	all := revs("newest", "middle", "oldest")
	got := filterRollbackCandidates(all, "newest", 10)
	if got[0].Name != "middle" || got[1].Name != "oldest" {
		t.Errorf("order broken: %+v", got)
	}
}

func TestFilterRollbackCandidates_CurrentNotPresent(t *testing.T) {
	// If 'current' isn't in the list, no exclusion happens.
	all := revs("a", "b", "c")
	got := filterRollbackCandidates(all, "nonexistent", 10)
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestFilterRollbackCandidates_SingleRevision(t *testing.T) {
	// Only one revision exists and it's the current one → no candidates.
	all := revs("only-one")
	got := filterRollbackCandidates(all, "only-one", 5)
	if len(got) != 0 {
		t.Errorf("single-current → expected empty, got %v", got)
	}
}

func TestFilterRollbackCandidates_EmptyInput(t *testing.T) {
	got := filterRollbackCandidates(nil, "anything", 5)
	if len(got) != 0 {
		t.Errorf("nil input → expected empty, got %v", got)
	}
}
