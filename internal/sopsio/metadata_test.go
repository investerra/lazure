package sopsio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Representative SOPS-encrypted YAML. Only the tail `sops:` block is read;
// the ENC[...] secret lines are there to prove the parser tolerates real
// file content.
const validFixture = `nexus-database-url: ENC[AES256_GCM,data:xxx,iv:yy,tag:zz,type:str]
nexus-redis-url: ENC[AES256_GCM,data:aaa,iv:bb,tag:cc,type:str]
sops:
    azure_kv:
        - vault_url: https://kv-example.vault.azure.net
          name: sops
          version: 3ca8dbe3e3b64932afbbf4b3661c0799
          created_at: "2026-04-10T14:46:00Z"
          enc: abcdef
    lastmodified: "2026-04-21T09:38:11Z"
    mac: ENC[AES256_GCM,data:qq,iv:rr,tag:ss,type:str]
    unencrypted_suffix: _unencrypted
    version: 3.12.2
`

func TestVaultURL_Valid(t *testing.T) {
	path := writeTempFile(t, validFixture)
	got, err := VaultURL(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://kv-example.vault.azure.net"
	if got != want {
		t.Errorf("VaultURL = %q, want %q", got, want)
	}
}

func TestVaultURL_MultipleEntries_ReturnsFirst(t *testing.T) {
	fixture := `foo: ENC[...]
sops:
    azure_kv:
        - vault_url: https://kv-first.vault.azure.net
          name: sops
        - vault_url: https://kv-second.vault.azure.net
          name: backup
    version: 3.12.2
`
	path := writeTempFile(t, fixture)
	got, err := VaultURL(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://kv-first.vault.azure.net" {
		t.Errorf("VaultURL = %q, want first entry", got)
	}
}

func TestVaultURL_Errors(t *testing.T) {
	cases := []struct {
		name       string
		fixture    string
		wantSubstr string
	}{
		{
			name:       "no sops block",
			fixture:    "foo: ENC[...]\n",
			wantSubstr: "no sops.azure_kv metadata",
		},
		{
			name: "empty azure_kv list",
			fixture: `foo: ENC[...]
sops:
    azure_kv: []
    version: 3.12.2
`,
			wantSubstr: "no sops.azure_kv metadata",
		},
		{
			name: "missing azure_kv key (e.g. age/gpg encryption)",
			fixture: `foo: ENC[...]
sops:
    age:
        - recipient: age1...
    version: 3.12.2
`,
			wantSubstr: "no sops.azure_kv metadata",
		},
		{
			name: "empty vault_url",
			fixture: `foo: ENC[...]
sops:
    azure_kv:
        - vault_url: ""
          name: sops
    version: 3.12.2
`,
			wantSubstr: "empty sops.azure_kv[0].vault_url",
		},
		{
			name:       "malformed yaml",
			fixture:    "sops: [not-a-map\n  foo bar\n",
			wantSubstr: "parse sops metadata",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, tc.fixture)
			_, err := VaultURL(path)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestVaultURL_FileNotFound(t *testing.T) {
	_, err := VaultURL("/nonexistent/path/secrets.yml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error = %q, want a read error", err.Error())
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.secrets.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}
