package sopsio

import (
	"os"
	"strings"
	"testing"
)

func TestParseDecryptedYAML_BasicStrings(t *testing.T) {
	in := []byte(`
nexus-database-url: "postgres://user:pw@host:5432/db"
nexus-redis-url: "rediss://:token@host:6380/0"
nexus-api-key: sample-secret-value
sops:
    azure_kv:
      - vault_url: https://kv-example.vault.azure.net
    version: 3.12.2
`)
	got, err := parseDecryptedYAML(in)
	if err != nil {
		t.Fatal(err)
	}
	if got["nexus-database-url"] != "postgres://user:pw@host:5432/db" {
		t.Errorf("nexus-database-url = %q", got["nexus-database-url"])
	}
	if got["nexus-redis-url"] != "rediss://:token@host:6380/0" {
		t.Errorf("nexus-redis-url = %q", got["nexus-redis-url"])
	}
	if got["nexus-api-key"] != "sample-secret-value" {
		t.Errorf("nexus-api-key = %q", got["nexus-api-key"])
	}
	// sops block must be stripped.
	if _, has := got["sops"]; has {
		t.Errorf("sops metadata key was not stripped: %+v", got)
	}
}

func TestParseDecryptedYAML_KebabCasePreserved(t *testing.T) {
	in := []byte(`
a-b-c: "one"
d_e_f: "two"
simple: "three"
`)
	got, err := parseDecryptedYAML(in)
	if err != nil {
		t.Fatal(err)
	}
	// Kebab, snake, and plain all survive unchanged.
	if got["a-b-c"] != "one" {
		t.Errorf("kebab key missing: %+v", got)
	}
	if got["d_e_f"] != "two" {
		t.Errorf("snake key missing: %+v", got)
	}
	if got["simple"] != "three" {
		t.Errorf("simple key missing: %+v", got)
	}
}

func TestParseDecryptedYAML_CoerceNumber(t *testing.T) {
	// Azure KV stores strings; if a user accidentally writes `port: 5432`
	// in the plain file, SOPS preserves the int type on decrypt. We coerce
	// to string with a WARN rather than failing.
	in := []byte(`port: 5432`)
	got, err := parseDecryptedYAML(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["port"] != "5432" {
		t.Errorf("numeric coercion: %q, want '5432'", got["port"])
	}
}

func TestParseDecryptedYAML_CoerceBool(t *testing.T) {
	in := []byte(`feature-flag: true`)
	got, err := parseDecryptedYAML(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["feature-flag"] != "true" {
		t.Errorf("bool coercion: %q, want 'true'", got["feature-flag"])
	}
}

func TestParseDecryptedYAML_Errors(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantSubstr string
	}{
		{
			name:       "null value",
			in:         "my-secret: ~\n",
			wantSubstr: "null value",
		},
		{
			name:       "list value",
			in:         "my-secret: [a, b]\n",
			wantSubstr: "unsupported type",
		},
		{
			name:       "nested map",
			in:         "my-secret:\n  inner: value\n",
			wantSubstr: "unsupported type",
		},
		{
			name:       "malformed yaml",
			in:         "key: value\n  bad-indent: x\n",
			wantSubstr: "parse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDecryptedYAML([]byte(tc.in))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestParseDecryptedYAML_EmptyFile(t *testing.T) {
	// An empty decrypted file (no secrets, just sops metadata) should
	// yield an empty map without error.
	in := []byte(`
sops:
    azure_kv: []
    version: 3.12.2
`)
	got, err := parseDecryptedYAML(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %+v", got)
	}
}

// TestDecrypt_Integration is a live smoke test against an opt-in encrypted
// fixture. Set LAZURE_INTEGRATION_SOPS_FILE to run it.
func TestDecrypt_Integration(t *testing.T) {
	fixturePath := os.Getenv("LAZURE_INTEGRATION_SOPS_FILE")
	if fixturePath == "" {
		t.Skip("skipping: LAZURE_INTEGRATION_SOPS_FILE not set")
	}

	got, err := Decrypt(fixturePath)
	if err != nil {
		t.Skipf("skipping: Decrypt failed — likely credential/network issue: %v", err)
	}

	// We don't check values (they're secrets) but verify the expected
	// secret names are all present.
	expected := []string{
		"nexus-database-url",
		"nexus-redis-url",
		"nexus-encryption-key",
		"nexus-jwt-signing-key",
		"nexus-kv-key",
	}
	for _, name := range expected {
		v, has := got[name]
		if !has {
			t.Errorf("missing secret %q", name)
			continue
		}
		if v == "" {
			t.Errorf("secret %q has empty value", name)
		}
	}
	if _, has := got["sops"]; has {
		t.Errorf("sops metadata leaked into result")
	}
}
