package lazurecfg

import (
	"os"
	"path/filepath"
	"testing"
)

// sharedSopsFixture is parseable as YAML and carries a different
// vault_url than testSecretsFixture so we can verify per-env wins
// when both files exist.
const sharedSopsFixture = `shared-secret: ENC[AES256_GCM,data:y,iv:y,tag:y,type:str]
sops:
    azure_kv:
        - vault_url: https://kv-shared.vault.azure.net
          name: sops
    version: 3.12.2
`

// ---------- LoadVaultURL ----------

func TestLoadVaultURL_BothMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	url, err := LoadVaultURL(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("both missing should not error: %v", err)
	}
	if url != "" {
		t.Errorf("url = %q, want empty when no secrets file exists", url)
	}
}

func TestLoadVaultURL_SharedOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SharedSecretsFile), []byte(sharedSopsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	url, err := LoadVaultURL(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://kv-shared.vault.azure.net" {
		t.Errorf("url = %q, want shared kv URL", url)
	}
}

func TestLoadVaultURL_PerEnvWinsOverShared(t *testing.T) {
	dir := setupProject(t) // creates envs/dev.secrets.yml with kv-test URL
	if err := os.WriteFile(filepath.Join(dir, SharedSecretsFile), []byte(sharedSopsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	url, err := LoadVaultURL(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://kv-test.vault.azure.net" {
		t.Errorf("url = %q, want per-env URL to win over shared", url)
	}
}

// ---------- LoadSecrets ----------

func TestLoadSecrets_BothMissingReturnsEmptyMap(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadSecrets(LoadOptions{ProjectDir: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("both missing should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty map", got)
	}
}

// ---------- SecretsPaths ----------

func TestSecretsPaths(t *testing.T) {
	shared, env := SecretsPaths("/proj", "dev")
	if shared != "/proj/secrets.yml" {
		t.Errorf("shared = %q", shared)
	}
	if env != "/proj/envs/dev.secrets.yml" {
		t.Errorf("env = %q", env)
	}
}
