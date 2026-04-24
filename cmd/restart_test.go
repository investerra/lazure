package cmd

import (
	"strings"
	"testing"

	"github.com/investerra/lazure/internal/azurearm"
)

func replica(name string, containersReady ...bool) azurearm.Replica {
	containers := make([]azurearm.ReplicaContainer, len(containersReady))
	for i, ready := range containersReady {
		containers[i] = azurearm.ReplicaContainer{Name: "c", Ready: ready}
	}
	return azurearm.Replica{
		Name:       name,
		Properties: azurearm.ReplicaProperties{Containers: containers},
	}
}

func TestReplicaNameSet(t *testing.T) {
	got := replicaNameSet([]azurearm.Replica{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	})
	if len(got) != 3 {
		t.Errorf("len = %d", len(got))
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, ok := got[name]; !ok {
			t.Errorf("missing %q", name)
		}
	}
}

func TestReplicaAllContainersReady(t *testing.T) {
	tests := []struct {
		name string
		r    azurearm.Replica
		want bool
	}{
		{"all ready", replica("x", true, true), true},
		{"one not ready", replica("x", true, false), false},
		{"none ready", replica("x", false, false), false},
		{"no containers at all", azurearm.Replica{Name: "x"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := replicaAllContainersReady(tc.r); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReplicaAllContainersReady_InitContainers(t *testing.T) {
	r := azurearm.Replica{
		Properties: azurearm.ReplicaProperties{
			Containers:     []azurearm.ReplicaContainer{{Ready: true}},
			InitContainers: []azurearm.ReplicaContainer{{Ready: false}},
		},
	}
	if replicaAllContainersReady(r) {
		t.Error("should be false — init container not ready")
	}
}

// restartComplete: the core of --wait's termination logic.

func TestRestartComplete_AllReplaced_AllReady(t *testing.T) {
	baseline := map[string]struct{}{"old-1": {}, "old-2": {}}
	current := []azurearm.Replica{
		replica("new-1", true),
		replica("new-2", true),
	}
	done, status := restartComplete(current, baseline)
	if !done {
		t.Errorf("should be done; status=%+v", status)
	}
	if status.baselineStillPresent != 0 || status.newReady != 2 || status.newTotal != 2 {
		t.Errorf("status = %+v", status)
	}
}

func TestRestartComplete_BaselineStillPresent(t *testing.T) {
	baseline := map[string]struct{}{"old-1": {}}
	current := []azurearm.Replica{
		replica("old-1", true), // still the old one
		replica("new-1", true),
	}
	done, status := restartComplete(current, baseline)
	if done {
		t.Errorf("should NOT be done when baseline still visible; status=%+v", status)
	}
	if status.baselineStillPresent != 1 {
		t.Errorf("baselineStillPresent = %d, want 1", status.baselineStillPresent)
	}
}

func TestRestartComplete_NewReplicaNotReady(t *testing.T) {
	baseline := map[string]struct{}{}
	current := []azurearm.Replica{
		replica("new-1", true),
		replica("new-2", false), // still starting
	}
	done, status := restartComplete(current, baseline)
	if done {
		t.Errorf("should NOT be done when new replica not ready")
	}
	if status.newReady != 1 || status.newTotal != 2 {
		t.Errorf("status = %+v", status)
	}
}

func TestRestartComplete_EmptyCurrentNotDone(t *testing.T) {
	// ACA briefly returns an empty replica list during cycling. We must
	// not treat that as "done" — keep polling until real replicas
	// appear.
	done, _ := restartComplete(nil, map[string]struct{}{"old": {}})
	if done {
		t.Error("empty current should NOT be done")
	}
}

func TestRestartComplete_FirstDeploy_NoBaseline(t *testing.T) {
	// Edge case: baseline was empty (app had no replicas before — shouldn't
	// happen in practice for restart but defensive).
	baseline := map[string]struct{}{}
	current := []azurearm.Replica{replica("new", true)}
	done, _ := restartComplete(current, baseline)
	if !done {
		t.Error("no baseline + all-ready current should be done")
	}
}

// ---------- findFirstNewReadyReplica ----------

func TestFindFirstNewReadyReplica_ReturnsFirstNewReady(t *testing.T) {
	baseline := map[string]struct{}{"old-1": {}}
	current := []azurearm.Replica{
		replica("old-1", true),        // still there, baseline — skip
		replica("new-a", true, false), // new but not fully ready
		replica("new-b", true),        // new + ready → pick this
		replica("new-c", true),        // also candidate but earlier wins
	}
	got, ok := findFirstNewReadyReplica(current, baseline)
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Name != "new-b" {
		t.Errorf("got %q, want new-b", got.Name)
	}
}

func TestFindFirstNewReadyReplica_NoNewReplicas(t *testing.T) {
	baseline := map[string]struct{}{"old-1": {}, "old-2": {}}
	current := []azurearm.Replica{replica("old-1", true), replica("old-2", true)}
	if _, ok := findFirstNewReadyReplica(current, baseline); ok {
		t.Error("baseline-only set should produce no match")
	}
}

func TestFindFirstNewReadyReplica_NewButNoneReady(t *testing.T) {
	baseline := map[string]struct{}{"old-1": {}}
	current := []azurearm.Replica{
		replica("old-1", true),
		replica("new-1", true, false), // new but container not ready
	}
	if _, ok := findFirstNewReadyReplica(current, baseline); ok {
		t.Error("new-but-not-ready replica should NOT be picked — we wait for Ready")
	}
}

func TestFindFirstNewReadyReplica_EmptyCurrent(t *testing.T) {
	baseline := map[string]struct{}{"old-1": {}}
	if _, ok := findFirstNewReadyReplica(nil, baseline); ok {
		t.Error("empty current replica list should produce no match")
	}
}

// ---------- findFirstReadyReplica (deploy/rollback) ----------

func TestFindFirstReadyReplica_FirstReady(t *testing.T) {
	replicas := []azurearm.Replica{
		replica("a", true, false), // not fully ready
		replica("b", true),        // ready → pick
		replica("c", true),        // also ready but earlier wins
	}
	r, ok := findFirstReadyReplica(replicas)
	if !ok {
		t.Fatal("expected match")
	}
	if r.Name != "b" {
		t.Errorf("got %q, want b", r.Name)
	}
}

func TestFindFirstReadyReplica_NoneReady(t *testing.T) {
	replicas := []azurearm.Replica{
		replica("a", false, true),
		replica("b", true, false),
	}
	if _, ok := findFirstReadyReplica(replicas); ok {
		t.Error("no replica fully ready → no match")
	}
}

func TestFindFirstReadyReplica_Empty(t *testing.T) {
	if _, ok := findFirstReadyReplica(nil); ok {
		t.Error("empty list → no match")
	}
}

// ---------- allRevisionReplicasReady ----------

func TestAllRevisionReplicasReady_AllReady(t *testing.T) {
	replicas := []azurearm.Replica{
		replica("a", true),
		replica("b", true, true),
	}
	if !allRevisionReplicasReady(replicas) {
		t.Error("all replicas fully ready should be complete")
	}
}

func TestAllRevisionReplicasReady_OneNotReady(t *testing.T) {
	replicas := []azurearm.Replica{
		replica("a", true),
		replica("b", true, false), // second container not ready
	}
	if allRevisionReplicasReady(replicas) {
		t.Error("any not-ready should fail completion")
	}
}

func TestAllRevisionReplicasReady_Empty(t *testing.T) {
	// Important: empty list stays non-done so the poll keeps going
	// during Azure's warmup window before replicas appear.
	if allRevisionReplicasReady(nil) {
		t.Error("empty list must not be considered done")
	}
}

// ---------- revisionReadyMessage ----------

func TestRevisionReadyMessage_NoReplicas(t *testing.T) {
	got := revisionReadyMessage(nil)
	if !strings.Contains(got, "appear") {
		t.Errorf("expected 'waiting for replicas to appear', got %q", got)
	}
}

func TestRevisionReadyMessage_Partial(t *testing.T) {
	replicas := []azurearm.Replica{replica("a", true), replica("b", true, false)}
	got := revisionReadyMessage(replicas)
	if !strings.Contains(got, "1/2") {
		t.Errorf("expected 1/2 progress, got %q", got)
	}
}

func TestRevisionReadyMessage_AllReady(t *testing.T) {
	replicas := []azurearm.Replica{replica("a", true), replica("b", true)}
	got := revisionReadyMessage(replicas)
	if !strings.Contains(got, "2/2") {
		t.Errorf("expected 2/2, got %q", got)
	}
}
