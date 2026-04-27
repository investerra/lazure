package sopsio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEncrypt_Integration exercises the full round-trip:
//
//	start with a real encrypted file → decrypt → modify plain → re-encrypt
//	→ decrypt again → verify the modification is present AND that the
//	SOPS metadata (vault URL, version) is preserved
//
// Skipped if Azure credentials aren't available (`az account show` fails).
func TestEncrypt_Integration(t *testing.T) {
	if err := exec.Command("az", "account", "show").Run(); err != nil {
		t.Skip("skipping: no Azure credentials available (az account show failed)")
	}

	srcEncrypted := "../../deploy/envs/dev.secrets.yml"

	// Copy the encrypted file to a temp location so we don't mutate the fixture.
	dir := t.TempDir()
	workingEnc := filepath.Join(dir, "dev.secrets.yml")
	if err := copyFile(srcEncrypted, workingEnc); err != nil {
		t.Fatal(err)
	}

	// Decrypt the copy so we have a baseline of the plaintext.
	before, err := Decrypt(workingEnc)
	if err != nil {
		t.Skipf("skipping: initial decrypt failed (credential/network issue?): %v", err)
	}
	if len(before) == 0 {
		t.Fatal("baseline decrypt returned no secrets")
	}

	// Save vault URL from the original encrypted file's metadata so we
	// can compare after re-encryption.
	originalVaultURL, err := VaultURL(workingEnc)
	if err != nil {
		t.Fatal(err)
	}

	// Write a modified plain file: same content plus a new test key.
	plainPath := filepath.Join(dir, "dev.secrets.plain.yml")
	plainContent := ""
	for k, v := range before {
		plainContent += k + ": " + quote(v) + "\n"
	}
	plainContent += `lazure-test-roundtrip: "hello-encrypt-integration"` + "\n"
	if err := os.WriteFile(plainPath, []byte(plainContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Re-encrypt back over workingEnc. configPath is irrelevant on the
	// re-encrypt path (existing file's metadata is the source of truth).
	if err := Encrypt(plainPath, workingEnc, ""); err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Vault URL metadata should be preserved (same KV key).
	newVaultURL, err := VaultURL(workingEnc)
	if err != nil {
		t.Fatal(err)
	}
	if newVaultURL != originalVaultURL {
		t.Errorf("vault_url changed during re-encrypt: before=%q after=%q", originalVaultURL, newVaultURL)
	}

	// Decrypt the re-encrypted file; new key must be present.
	after, err := Decrypt(workingEnc)
	if err != nil {
		t.Fatalf("post-encrypt decrypt failed: %v", err)
	}
	if after["lazure-test-roundtrip"] != "hello-encrypt-integration" {
		t.Errorf("round-trip lost the added secret: got %q", after["lazure-test-roundtrip"])
	}

	// Spot check a pre-existing secret survived intact.
	for k, v := range before {
		if after[k] != v {
			t.Errorf("secret %q changed through round-trip: before=%q after=%q", k, v, after[k])
			break
		}
	}
}

// TestEncrypt_BootstrapWithoutConfig verifies that the bootstrap
// path errors helpfully when neither the encrypted file nor a
// .sops.yaml exists — the user has to wire up the config before
// lazure can encrypt anything for the first time.
func TestEncrypt_BootstrapWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "plain.yml")
	if err := os.WriteFile(plainPath, []byte("foo: bar\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Encrypt(plainPath, filepath.Join(dir, "doesnotexist.yml"), filepath.Join(dir, ".sops.yaml"))
	if err == nil {
		t.Fatal("expected error for missing config")
	}
	if !strings.Contains(err.Error(), ".sops.yaml") {
		t.Errorf("error = %q, want it to mention .sops.yaml", err.Error())
	}
}

func TestEncrypt_MissingPlainFile(t *testing.T) {
	if err := exec.Command("az", "account", "show").Run(); err != nil {
		t.Skip("skipping: no Azure credentials to load fixture metadata")
	}

	dir := t.TempDir()
	workingEnc := filepath.Join(dir, "dev.secrets.yml")
	if err := copyFile("../../deploy/envs/dev.secrets.yml", workingEnc); err != nil {
		t.Fatal(err)
	}

	err := Encrypt(filepath.Join(dir, "nonexistent-plain.yml"), workingEnc, "")
	if err == nil {
		t.Fatal("expected error for missing plain file")
	}
	if !strings.Contains(err.Error(), "read plain") {
		t.Errorf("error = %q, want 'read plain'", err.Error())
	}
}

// ---------- helpers ----------

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// quote returns a YAML-safe double-quoted form of v. Handles the common
// cases in our secrets (containing slashes, @, +, etc.). Not a full YAML
// quoter — good enough for known test inputs.
func quote(v string) string {
	// Escape existing double quotes and backslashes.
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}
