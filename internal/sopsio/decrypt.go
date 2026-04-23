package sopsio

import (
	"fmt"
	"log/slog"

	"github.com/getsops/sops/v3/decrypt"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/errs"
)

// Decrypt returns the plaintext secret key-value pairs from a SOPS-encrypted
// YAML file. The `sops:` metadata block is stripped from the result.
//
// Values must be scalar strings; numbers and bools are coerced to string
// with a WARN log. Other shapes (nested maps, lists, null) error.
//
// Decryption uses SOPS's built-in Azure Key Vault client which picks up
// credentials via the same DefaultAzureCredential chain lazure uses
// elsewhere: env vars → managed identity → az CLI → VS Code.
func Decrypt(path string) (map[string]string, error) {
	slog.Debug("sopsio: decrypting file", "path", path)
	plain, err := decrypt.File(path, "yaml")
	if err != nil {
		return nil, errs.Wrapf(err, "sopsio: decrypt %s", path)
	}
	slog.Debug("sopsio: decrypted, parsing YAML", "bytes", len(plain))
	out, err := parseDecryptedYAML(plain)
	if err != nil {
		return nil, err
	}
	slog.Debug("sopsio: decryption complete", "path", path, "secret_count", len(out))
	return out, nil
}

// parseDecryptedYAML converts decrypted SOPS YAML bytes into a string map,
// stripping the `sops:` metadata and coercing non-string scalars.
//
// Split from Decrypt so it can be unit-tested without needing Azure Key
// Vault credentials.
func parseDecryptedYAML(data []byte) (map[string]string, error) {
	raw := map[string]any{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, errs.Wrap(err, "sopsio: parse decrypted YAML")
	}

	// The `sops:` metadata block is always present in a decrypted file
	// (SOPS preserves it on decrypt so the file round-trips). Drop it
	// before returning to the caller — it isn't a real secret.
	delete(raw, "sops")

	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, err := coerceString(k, v)
		if err != nil {
			return nil, err
		}
		out[k] = s
	}
	return out, nil
}

// coerceString turns a decoded YAML scalar into a string. Bools and
// numbers get coerced with a WARN so users get feedback that their
// secrets file has unexpected types (Azure KV stores strings only).
func coerceString(key string, v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		slog.Warn("sopsio: coercing bool to string", "secret", key, "value", t)
		return fmt.Sprintf("%t", t), nil
	case int, int64, float64:
		slog.Warn("sopsio: coercing number to string", "secret", key, "value", t)
		return fmt.Sprintf("%v", t), nil
	case nil:
		return "", errs.Errorf("sopsio: secret %q has null value; Azure Key Vault requires a string", key)
	default:
		return "", errs.Errorf("sopsio: secret %q has unsupported type %T; Azure Key Vault secrets must be strings", key, v)
	}
}
