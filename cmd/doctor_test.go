package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ---------- parseGitVersion ----------

func TestParseGitVersion_Standard(t *testing.T) {
	got := parseGitVersion("git version 2.43.0\n")
	if got != "2.43.0" {
		t.Errorf("got %q, want 2.43.0", got)
	}
}

func TestParseGitVersion_Unusual(t *testing.T) {
	got := parseGitVersion("weird-output-without-prefix")
	if got != "weird-output-without-prefix" {
		t.Errorf("got %q, want pass-through", got)
	}
}

// ---------- checkEditor ----------

func TestCheckEditor_PrefersEditor(t *testing.T) {
	t.Setenv("EDITOR", "vim")
	t.Setenv("VISUAL", "nano")
	r := checkEditor()
	if r.status != statusPass {
		t.Fatalf("status = %v, want pass", r.status)
	}
	if !strings.Contains(r.detail, "vim") {
		t.Errorf("detail = %q, should reference EDITOR (vim)", r.detail)
	}
	if strings.Contains(r.detail, "nano") {
		t.Errorf("detail = %q, should NOT reference VISUAL when EDITOR is set", r.detail)
	}
}

func TestCheckEditor_FallsBackToVisual(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "nano")
	r := checkEditor()
	if r.status != statusPass {
		t.Fatalf("status = %v, want pass", r.status)
	}
	if !strings.Contains(r.detail, "nano") {
		t.Errorf("detail = %q, should reference VISUAL (nano)", r.detail)
	}
}

func TestCheckEditor_BothEmpty(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")
	r := checkEditor()
	if r.status != statusFail {
		t.Errorf("status = %v, want fail", r.status)
	}
}

// ---------- checkGH / checkAz status levels ----------

// TestOptionalBinaryChecksAreWarnNotFail locks in that az and gh
// missing-binary paths produce WARN rather than FAIL — they're needed
// only by specific commands (exec, release --wait) and must not break
// `doctor` for users who never touch those commands.
func TestOptionalBinaryChecksAreWarnNotFail(t *testing.T) {
	// Shadow PATH so LookPath fails for our test.
	t.Setenv("PATH", "/definitely-not-a-path")
	for _, tc := range []struct {
		name  string
		check func() checkResult
	}{
		{"az", checkAz},
		{"gh", checkGH},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.check()
			if r.status != statusWarn {
				t.Errorf("%s missing should WARN, got %v", tc.name, r.status)
			}
			if !strings.Contains(r.detail, "not installed") {
				t.Errorf("%s detail = %q, expected 'not installed' message", tc.name, r.detail)
			}
		})
	}
}

// ---------- findManifest ----------

func TestFindManifest_PrefersDeployYml(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lazure.yml"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := findManifest(dir)
	if filepath.Base(got) != "deploy.yml" {
		t.Errorf("got %q, want deploy.yml preferred", got)
	}
}

func TestFindManifest_FallsBackToLazureYml(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lazure.yml"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := findManifest(dir)
	if filepath.Base(got) != "lazure.yml" {
		t.Errorf("got %q, want lazure.yml fallback", got)
	}
}

func TestFindManifest_MissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := findManifest(dir); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFindManifest_DirectoryNamedDeployYmlIgnored(t *testing.T) {
	// Defensive: a directory named `deploy.yml` must not be mistaken for
	// a manifest file.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "deploy.yml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findManifest(dir); got != "" {
		t.Errorf("got %q, want empty (directory must not match)", got)
	}
}

// ---------- discoverEnvs ----------

func TestDiscoverEnvs_BasicGlob(t *testing.T) {
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	_ = os.Mkdir(envsDir, 0o755)
	for _, f := range []string{"dev.vars.yml", "uat.vars.yml", "prod.vars.yml"} {
		if err := os.WriteFile(filepath.Join(envsDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := discoverEnvs(dir)
	want := []string{"dev", "prod", "uat"} // sorted
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDiscoverEnvs_IgnoresPlainAndSecrets(t *testing.T) {
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	_ = os.Mkdir(envsDir, 0o755)
	for _, f := range []string{
		"dev.vars.yml",
		"dev.secrets.yml",
		"dev.plain.yml",
		"dev.secrets.plain.yml",
		"uat.vars.yml",
	} {
		if err := os.WriteFile(filepath.Join(envsDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := discoverEnvs(dir)
	want := []string{"dev", "uat"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDiscoverEnvs_NoEnvsDir(t *testing.T) {
	dir := t.TempDir()
	if got := discoverEnvs(dir); len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// ---------- anyFailed ----------

func TestAnyFailed_AllPass(t *testing.T) {
	s := []section{{
		name: "x",
		results: []checkResult{
			{status: statusPass}, {status: statusPass},
		},
	}}
	if anyFailed(s) {
		t.Error("want false")
	}
}

func TestAnyFailed_WarnDoesNotFail(t *testing.T) {
	s := []section{{
		name: "x",
		results: []checkResult{
			{status: statusPass}, {status: statusWarn},
		},
	}}
	if anyFailed(s) {
		t.Error("warn must not cause overall failure")
	}
}

func TestAnyFailed_TopLevelFailFound(t *testing.T) {
	s := []section{{
		name: "x",
		results: []checkResult{
			{status: statusPass}, {status: statusFail},
		},
	}}
	if !anyFailed(s) {
		t.Error("want true")
	}
}

func TestAnyFailed_EnvCheckFailFound(t *testing.T) {
	s := []section{{
		name:      "project",
		envChecks: []envCheck{{env: "dev", status: statusFail}},
	}}
	if !anyFailed(s) {
		t.Error("env-level fail must bubble up to anyFailed")
	}
}

// ---------- renderDoctorText ----------

func TestRenderDoctorText_AllPassPlain(t *testing.T) {
	s := []section{{
		name: "global checks",
		results: []checkResult{
			{status: statusPass, name: "git", detail: "2.43.0"},
			{status: statusPass, name: "editor", detail: "$EDITOR=vim"},
		},
	}}
	got := renderDoctorText(s, false)
	for _, want := range []string{
		"global checks", "[✓]", "git", "2.43.0", "editor", "$EDITOR=vim", "all checks passed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("color=false should suppress ANSI escapes; got:\n%s", got)
	}
}

func TestRenderDoctorText_WarnHasBangMark(t *testing.T) {
	s := []section{{
		name:    "global",
		results: []checkResult{{status: statusWarn, name: "az", detail: "not installed"}},
	}}
	got := renderDoctorText(s, false)
	if !strings.Contains(got, "[!]") {
		t.Errorf("expected [!] for warn, got:\n%s", got)
	}
	if !strings.Contains(got, "all checks passed") {
		t.Errorf("warn alone should still pass overall")
	}
}

func TestRenderDoctorText_FailShowsDetailContinuation(t *testing.T) {
	s := []section{{
		name: "project checks",
		results: []checkResult{
			{status: statusPass, name: "manifest", detail: "found at deploy/deploy.yml"},
		},
		envChecks: []envCheck{
			{
				env:     "uat",
				status:  statusFail,
				overall: "vars ✓  secrets decrypt ✗  manifest renders —  KV reachable —",
				failMsg: "sops decrypt: key vault permission denied",
			},
		},
	}}
	got := renderDoctorText(s, false)
	for _, want := range []string{
		"[✗]", "uat", "secrets decrypt ✗", "└─", "permission denied", "one or more checks failed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderDoctorText_Color(t *testing.T) {
	// Force lipgloss to emit ANSI even under `go test` (stdout is a pipe
	// → termenv auto-detect returns NoTTY → styles render as plain).
	prev := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.DefaultRenderer().SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.DefaultRenderer().SetColorProfile(prev) })

	s := []section{{
		name:    "x",
		results: []checkResult{{status: statusFail, name: "n", detail: "bad"}},
	}}
	got := renderDoctorText(s, true)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected ANSI escapes when color=true; got:\n%s", got)
	}
}

// ---------- renderDoctorJSON ----------

func TestRenderDoctorJSON_Shape(t *testing.T) {
	s := []section{{
		name: "global checks",
		results: []checkResult{
			{status: statusPass, name: "git", detail: "2.43.0"},
			{status: statusFail, name: "azure auth", detail: "no token"},
		},
	}, {
		name: "project checks",
		envChecks: []envCheck{
			{env: "dev", status: statusPass, overall: "everything ✓"},
			{env: "uat", status: statusFail, overall: "vars ✓  secrets decrypt ✗", failMsg: "denied"},
		},
	}}
	out, err := renderDoctorJSON(s)
	if err != nil {
		t.Fatal(err)
	}
	var parsed doctorJSONOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if parsed.Passed {
		t.Error("Passed should be false when any check failed")
	}
	if parsed.FailedCount != 2 { // azure auth + uat env
		t.Errorf("FailedCount = %d, want 2", parsed.FailedCount)
	}
	if len(parsed.Sections) != 2 {
		t.Fatalf("Sections len = %d, want 2", len(parsed.Sections))
	}
	if parsed.Sections[0].Checks[0].Status != "pass" {
		t.Errorf("status encoded as %q, want 'pass'", parsed.Sections[0].Checks[0].Status)
	}
}

func TestRenderDoctorJSON_AllPassed(t *testing.T) {
	s := []section{{
		name:    "x",
		results: []checkResult{{status: statusPass, name: "git", detail: "2"}},
	}}
	out, _ := renderDoctorJSON(s)
	var parsed doctorJSONOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.Passed {
		t.Error("Passed should be true when all pass")
	}
	if parsed.FailedCount != 0 {
		t.Errorf("FailedCount = %d, want 0", parsed.FailedCount)
	}
}

// ---------- runProjectChecks ----------

func TestRunProjectChecks_NoManifestReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if got := runProjectChecks(t.Context(), dir); got != nil {
		t.Errorf("expected nil section when no manifest; got %+v", got)
	}
}

func TestRunProjectChecks_NoEnvsWarns(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sec := runProjectChecks(t.Context(), dir)
	if sec == nil {
		t.Fatal("expected section")
	}
	foundWarn := false
	for _, r := range sec.results {
		if r.status == statusWarn && r.name == "envs" {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected 'envs' warn, got %+v", sec.results)
	}
}
