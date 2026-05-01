package lazurecfg

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testSecretsFixture = `nexus-database-url: ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]
sops:
    azure_kv:
        - vault_url: https://kv-test.vault.azure.net
          name: sops
          version: 0
    version: 3.12.2
`

func TestStandardVars_WithGitRepo(t *testing.T) {
	dir := setupProject(t)
	initGitRepo(t, dir)
	commitAll(t, dir, "initial")

	vars, err := StandardVars(dir, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if vars["app_env"] != "dev" {
		t.Errorf("app_env = %v, want %q", vars["app_env"], "dev")
	}
	if vars["keyvault_url"] != "https://kv-test.vault.azure.net" {
		t.Errorf("keyvault_url = %v", vars["keyvault_url"])
	}
	if got := vars["git_branch"].(string); got != "main" {
		t.Errorf("git_branch = %q, want %q", got, "main")
	}
	if got := vars["git_commit"].(string); len(got) != 40 {
		t.Errorf("git_commit length = %d, want 40 (full SHA)", len(got))
	}
	if got := vars["git_short_commit"].(string); len(got) < 7 || strings.HasSuffix(got, "-dirty") {
		t.Errorf("git_short_commit = %q, want clean 7+ chars", got)
	}
	if got := vars["git_dirty"].(bool); got {
		t.Errorf("git_dirty = true, want false for clean tree")
	}
}

func TestStandardVars_DirtyRepo(t *testing.T) {
	dir := setupProject(t)
	initGitRepo(t, dir)
	commitAll(t, dir, "initial")
	// Create an untracked file → tree is dirty (without breaking the
	// already-valid SOPS fixture).
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	vars, err := StandardVars(dir, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := vars["git_dirty"].(bool); !got {
		t.Errorf("git_dirty = false, want true")
	}
	if got := vars["git_short_commit"].(string); !strings.HasSuffix(got, "-dirty") {
		t.Errorf("git_short_commit = %q, want -dirty suffix", got)
	}
	if got := vars["git_commit"].(string); strings.HasSuffix(got, "-dirty") {
		t.Errorf("git_commit = %q, must NOT have -dirty suffix", got)
	}
}

func TestStandardVars_NotAGitRepo(t *testing.T) {
	dir := setupProject(t)
	// No git init.
	vars, err := StandardVars(dir, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if vars["git_branch"] != "" {
		t.Errorf("git_branch = %v, want empty", vars["git_branch"])
	}
	if vars["git_commit"] != "" {
		t.Errorf("git_commit = %v, want empty", vars["git_commit"])
	}
	if vars["git_short_commit"] != "" {
		t.Errorf("git_short_commit = %v, want empty", vars["git_short_commit"])
	}
	if vars["git_dirty"] != false {
		t.Errorf("git_dirty = %v, want false", vars["git_dirty"])
	}
	// keyvault_url still populated because the fixture exists.
	if vars["keyvault_url"] != "https://kv-test.vault.azure.net" {
		t.Errorf("keyvault_url = %v, should still work without git", vars["keyvault_url"])
	}
}

// Missing secrets file is no longer an error: keyvault_url just resolves
// to "" so callers can still render templates and run validate / view
// commands. Commands that genuinely need a vault (secrets sync, deploys
// referencing secrets) error at use site.
func TestStandardVars_MissingSecrets_ReturnsEmptyVaultURL(t *testing.T) {
	dir := t.TempDir()
	vars, err := StandardVars(dir, "dev")
	if err != nil {
		t.Fatalf("missing secrets file should not error, got: %v", err)
	}
	if vars["keyvault_url"] != "" {
		t.Errorf("keyvault_url = %v, want empty when secrets file is missing", vars["keyvault_url"])
	}
	if vars["app_env"] != "dev" {
		t.Errorf("app_env = %v, want dev", vars["app_env"])
	}
}

// ---------- helpers ----------

func setupProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	envsDir := filepath.Join(dir, "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, "dev.secrets.yml"), []byte(testSecretsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	// Disable gpg signing in case the machine has it set to required.
	runGit(t, dir, "config", "commit.gpgsign", "false")
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", msg)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\noutput: %s", strings.Join(args, " "), err, out)
	}
}
