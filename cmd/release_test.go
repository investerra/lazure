package cmd

import (
	"strings"
	"testing"
	"time"
)

// ---------- computeNextTag ----------

func TestComputeNextTag_FirstOfDay(t *testing.T) {
	today := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	got := computeNextTag(today, nil)
	if got != "v20260423.1" {
		t.Errorf("got %q, want v20260423.1", got)
	}
}

func TestComputeNextTag_IncrementsToday(t *testing.T) {
	today := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	existing := []string{"v20260423.1", "v20260423.2"}
	got := computeNextTag(today, existing)
	if got != "v20260423.3" {
		t.Errorf("got %q, want v20260423.3", got)
	}
}

func TestComputeNextTag_IgnoresOtherDays(t *testing.T) {
	today := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	existing := []string{"v20260422.1", "v20260422.5", "v20260420.9"}
	got := computeNextTag(today, existing)
	if got != "v20260423.1" {
		t.Errorf("got %q, want v20260423.1 (prior days must not bump today)", got)
	}
}

func TestComputeNextTag_Gaps(t *testing.T) {
	today := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	existing := []string{"v20260423.1", "v20260423.3"} // .2 deleted
	got := computeNextTag(today, existing)
	if got != "v20260423.4" {
		t.Errorf("got %q, want v20260423.4 (max+1, no gap-fill)", got)
	}
}

func TestComputeNextTag_IgnoresNonCalver(t *testing.T) {
	today := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	existing := []string{"v1.0.0", "rc-2", "something-else", "v20260423.1"}
	got := computeNextTag(today, existing)
	if got != "v20260423.2" {
		t.Errorf("got %q, want v20260423.2", got)
	}
}

// ---------- formatChangelog ----------

func TestFormatChangelog_Normal(t *testing.T) {
	logOut := "- fix: login redirect\n- feat: add logs command\n- chore: bump deps"
	got := formatChangelog(logOut, false, 0)
	want := "- fix: login redirect\n- feat: add logs command\n- chore: bump deps"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatChangelog_Empty(t *testing.T) {
	if got := formatChangelog("", false, 0); got != "(no changes)" {
		t.Errorf("got %q, want (no changes)", got)
	}
	if got := formatChangelog("   \n  ", false, 0); got != "(no changes)" {
		t.Errorf("got %q, want (no changes)", got)
	}
}

func TestFormatChangelog_FirstReleaseUnderCap(t *testing.T) {
	logOut := "- first\n- second\n- third"
	got := formatChangelog(logOut, true, 3)
	if got != logOut {
		t.Errorf("got %q, want unchanged (under cap)", got)
	}
	if strings.Contains(got, "... and") {
		t.Errorf("got %q, should not include cap suffix", got)
	}
}

func TestFormatChangelog_FirstReleaseOverCap(t *testing.T) {
	var lines []string
	for i := range firstReleaseCap + 1 { // one past the cap
		lines = append(lines, "- commit "+itoa(i))
	}
	logOut := strings.Join(lines, "\n")
	got := formatChangelog(logOut, true, 75)

	gotLines := strings.Split(got, "\n")
	if len(gotLines) != firstReleaseCap+1 { // 50 real + 1 "... and N more"
		t.Errorf("got %d lines, want %d", len(gotLines), firstReleaseCap+1)
	}
	if !strings.HasSuffix(got, "... and 25 more") {
		t.Errorf("missing cap suffix in output:\n%s", got)
	}
}

// TestFormatChangelog_ShallowCloneNoPanic regression-guards the
// slice-bounds bug: rev-list can report many more commits than the
// actual log produced (shallow clones, git options that truncate
// log). Slicing lines[:firstReleaseCap] without bounding the cap by
// len(lines) would panic at release time.
func TestFormatChangelog_ShallowCloneNoPanic(t *testing.T) {
	// Pretend we have 5 lines but rev-list says 9999.
	logOut := "- a\n- b\n- c\n- d\n- e"
	got := formatChangelog(logOut, true, 9999)

	if !strings.Contains(got, "- a") || !strings.Contains(got, "- e") {
		t.Errorf("expected all 5 lines preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "... and 9994 more") {
		t.Errorf("expected 'and 9994 more' suffix (9999 - 5 actual), got:\n%s", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ---------- renderPreview ----------

func TestRenderPreview_Normal(t *testing.T) {
	out := renderPreview("v20260423.1", "v20260422.3", "- a\n- b")
	for _, want := range []string{"v20260423.1", "v20260422.3", "- a", "- b", "since:", "new tag:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in preview:\n%s", want, out)
		}
	}
}

func TestRenderPreview_FirstRelease(t *testing.T) {
	out := renderPreview("v20260423.1", "", "- initial commit")
	if !strings.Contains(out, "first release") {
		t.Errorf("missing first-release marker:\n%s", out)
	}
}

// ---------- composeTagBody ----------

func TestComposeTagBody_WithHeader(t *testing.T) {
	got := composeTagBody("Bugfix release", "- fix: X")
	want := "Bugfix release\n\n- fix: X"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComposeTagBody_NoHeader(t *testing.T) {
	got := composeTagBody("", "- fix: X")
	if got != "- fix: X" {
		t.Errorf("got %q, want %q", got, "- fix: X")
	}
}

// ---------- parseGHRunListJSON ----------

func TestParseGHRunListJSON_Happy(t *testing.T) {
	b := []byte(`[
		{"databaseId":111,"workflowName":"release","status":"in_progress","conclusion":"","url":"https://x/111"},
		{"databaseId":222,"workflowName":"lint","status":"completed","conclusion":"success","url":"https://x/222"}
	]`)
	runs, err := parseGHRunListJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].WorkflowName != "release" || runs[0].Status != "in_progress" {
		t.Errorf("run[0] = %+v", runs[0])
	}
	if runs[1].Conclusion != "success" {
		t.Errorf("run[1] = %+v", runs[1])
	}
}

func TestParseGHRunListJSON_Empty(t *testing.T) {
	for _, in := range [][]byte{[]byte("[]"), []byte(""), []byte("null"), []byte("  ")} {
		runs, err := parseGHRunListJSON(in)
		if err != nil {
			t.Errorf("unexpected error for %q: %v", in, err)
		}
		if len(runs) != 0 {
			t.Errorf("expected empty slice for %q, got %v", in, runs)
		}
	}
}

// ---------- classifyConclusion ----------

func TestClassifyConclusion(t *testing.T) {
	cases := []struct {
		status, conclusion string
		want               runState
	}{
		{"queued", "", runPending},
		{"in_progress", "", runPending},
		{"completed", "success", runPassed},
		{"completed", "skipped", runPassed},
		{"completed", "neutral", runPassed},
		{"completed", "failure", runFailed},
		{"completed", "cancelled", runFailed},
		{"completed", "timed_out", runFailed},
		{"completed", "startup_failure", runFailed},
	}
	for _, tc := range cases {
		if got := classifyConclusion(tc.status, tc.conclusion); got != tc.want {
			t.Errorf("classifyConclusion(%q,%q) = %v, want %v",
				tc.status, tc.conclusion, got, tc.want)
		}
	}
}

// ---------- formatWatchStatusLine ----------

func TestFormatWatchStatusLine_StateChangedFirstTime(t *testing.T) {
	prev := map[int64]string{}
	r := ghRun{DatabaseID: 111, WorkflowName: "release", Status: "queued"}
	line, changed := formatWatchStatusLine(4*time.Second, r, prev)
	if !changed {
		t.Fatal("expected changed=true on first sighting")
	}
	for _, want := range []string{"[0:04]", "release", "queued"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
	if prev[111] == "" {
		t.Error("prev not updated")
	}
}

func TestFormatWatchStatusLine_StateUnchanged(t *testing.T) {
	prev := map[int64]string{111: "in_progress/"}
	r := ghRun{DatabaseID: 111, WorkflowName: "release", Status: "in_progress"}
	line, changed := formatWatchStatusLine(10*time.Second, r, prev)
	if changed || line != "" {
		t.Errorf("expected unchanged, got line=%q changed=%v", line, changed)
	}
}

func TestFormatWatchStatusLine_CompletedUsesConclusion(t *testing.T) {
	prev := map[int64]string{}
	r := ghRun{DatabaseID: 111, WorkflowName: "release", Status: "completed", Conclusion: "success"}
	line, _ := formatWatchStatusLine(137*time.Second, r, prev)
	if !strings.Contains(line, "success") || !strings.Contains(line, "✓") {
		t.Errorf("expected 'success ✓' in %q", line)
	}
	if strings.Contains(line, "completed") {
		t.Errorf("should show conclusion, not status: %q", line)
	}
}

func TestFormatWatchStatusLine_FailureGetsX(t *testing.T) {
	prev := map[int64]string{}
	r := ghRun{DatabaseID: 222, WorkflowName: "deploy", Status: "completed", Conclusion: "failure"}
	line, _ := formatWatchStatusLine(62*time.Second, r, prev)
	if !strings.Contains(line, "failure") || !strings.Contains(line, "✗") {
		t.Errorf("expected 'failure ✗' in %q", line)
	}
}

// ---------- allTerminal ----------

func TestAllTerminal(t *testing.T) {
	if !allTerminal([]ghRun{{Status: "completed"}, {Status: "completed"}}) {
		t.Error("all completed should be terminal")
	}
	if allTerminal([]ghRun{{Status: "completed"}, {Status: "in_progress"}}) {
		t.Error("one in_progress must make set non-terminal")
	}
	// Vacuously true — callers check len(runs) > 0 separately before
	// relying on allTerminal's verdict. Documented here as a known
	// edge case rather than asserted.
	_ = allTerminal(nil)
}

// ---------- firstFailedStep ----------

func TestFirstFailedStep_FindsFailedStepInFailedJob(t *testing.T) {
	jobs := []ghJob{
		{Name: "build", Conclusion: "success", Steps: []ghStep{{Name: "checkout", Conclusion: "success"}}},
		{Name: "deploy", Conclusion: "failure", Steps: []ghStep{
			{Name: "login", Conclusion: "success"},
			{Name: "upload", Conclusion: "failure"},
		}},
	}
	step, job := firstFailedStep(jobs)
	if job.Name != "deploy" {
		t.Errorf("job = %q, want deploy", job.Name)
	}
	if step.Name != "upload" {
		t.Errorf("step = %q, want upload", step.Name)
	}
}

func TestFirstFailedStep_NoFailedStep_ReturnsJobOnly(t *testing.T) {
	// Job can be marked failure with no step breakdown (e.g. runner crash).
	jobs := []ghJob{
		{Name: "deploy", Conclusion: "failure", Steps: nil},
	}
	step, job := firstFailedStep(jobs)
	if step.Name != "" {
		t.Errorf("step should be empty, got %q", step.Name)
	}
	if job.Name != "deploy" {
		t.Errorf("job = %q, want deploy", job.Name)
	}
}

func TestFirstFailedStep_AllPassed_ReturnsZero(t *testing.T) {
	jobs := []ghJob{{Name: "x", Conclusion: "success"}}
	step, job := firstFailedStep(jobs)
	if step.Name != "" || job.Name != "" {
		t.Errorf("expected zero values, got step=%+v job=%+v", step, job)
	}
}
