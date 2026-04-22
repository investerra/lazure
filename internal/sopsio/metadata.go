// Package sopsio wraps SOPS-encrypted secrets handling for Lazure: metadata
// extraction, decryption, and re-encryption preserving the Azure Key Vault
// master key binding.
package sopsio

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

// sopsMetadata is a minimal view of the `sops:` block that trails every
// SOPS-encrypted YAML file. Only the Azure KV entry we care about is
// modeled; anything else in that block (mac, lastmodified, etc.) is ignored.
type sopsMetadata struct {
	SOPS struct {
		AzureKV []struct {
			VaultURL string `json:"vault_url"`
		} `json:"azure_kv"`
	} `json:"sops"`
}

// VaultURL reads the Azure Key Vault URL from the SOPS metadata trailer of
// an encrypted secrets file. The file body stays encrypted — we only parse
// the plain-text `sops:` stanza.
//
// Returns an error if the file can't be read, the YAML is malformed, or
// there is no `sops.azure_kv[0].vault_url` entry.
func VaultURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("sopsio: read %s: %w", path, err)
	}
	return vaultURLFromBytes(data, path)
}

func vaultURLFromBytes(data []byte, origin string) (string, error) {
	var meta sopsMetadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("sopsio: parse sops metadata in %s: %w", origin, err)
	}
	if len(meta.SOPS.AzureKV) == 0 {
		return "", fmt.Errorf("sopsio: %s has no sops.azure_kv metadata (not encrypted with Azure Key Vault?)", origin)
	}
	url := meta.SOPS.AzureKV[0].VaultURL
	if url == "" {
		return "", fmt.Errorf("sopsio: %s has empty sops.azure_kv[0].vault_url", origin)
	}
	return url, nil
}
