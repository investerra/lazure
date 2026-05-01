package lazurecfg

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"

	"github.com/investerra/lazure/internal/sopsio"
)

// SharedSecretsFile is the conventional name for SOPS-encrypted
// secrets shared across every environment. Lives next to deploy.yml.
// Optional.
const SharedSecretsFile = "secrets.yml"

// SharedSecretsPath returns the conventional path for the project-wide
// shared SOPS-encrypted secrets file: <dir>/secrets.yml. May not
// exist on disk; callers must accept absence.
func SharedSecretsPath(projectDir string) string {
	return filepath.Join(projectDir, SharedSecretsFile)
}

// EnvSecretsPath returns the conventional path for a per-env
// SOPS-encrypted secrets file: <dir>/envs/<env>.secrets.yml. May not
// exist on disk; callers must accept absence.
func EnvSecretsPath(projectDir, env string) string {
	return filepath.Join(projectDir, "envs", env+".secrets.yml")
}

// SecretsPaths returns both canonical paths Lazure looks for, in
// loading order (lower precedence first):
//
//   - shared:  <dir>/secrets.yml
//   - per-env: <dir>/envs/<env>.secrets.yml
//
// Either or both may be absent on disk; callers should accept that.
// Prefer SharedSecretsPath / EnvSecretsPath when only one half is
// needed — passing an empty env to SecretsPaths produces a malformed
// per-env path.
func SecretsPaths(projectDir, env string) (shared, perEnv string) {
	return SharedSecretsPath(projectDir), EnvSecretsPath(projectDir, env)
}

// LoadSecrets returns the effective decrypted secrets map for env:
// the project-wide shared file (if present) overlaid with the per-env
// file (if present), per-env keys winning on conflict. Returns an
// empty map and no error when both files are absent — secrets are
// optional all the way down.
//
// Each file decrypts independently using its own SOPS metadata, so
// the two sides can be encrypted with different master keys (typical
// for projects that keep some secrets in a global vault and others
// per-env).
func LoadSecrets(opts LoadOptions) (map[string]string, error) {
	out := map[string]string{}
	if err := mergeSecretsFile(SharedSecretsPath(opts.ProjectDir), out, "shared"); err != nil {
		return nil, err
	}
	if err := mergeSecretsFile(EnvSecretsPath(opts.ProjectDir, opts.Env), out, "env"); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadVaultURL resolves the Azure Key Vault URL that should be used
// for this env's secret resolution. Per-env metadata wins when both
// files exist. Returns "" (no error) when neither file is present —
// callers that need a real vault should check and surface their own
// error at use site.
func LoadVaultURL(opts LoadOptions) (string, error) {
	if u, err := vaultURLIfPresent(EnvSecretsPath(opts.ProjectDir, opts.Env)); err != nil {
		return "", err
	} else if u != "" {
		return u, nil
	}
	if u, err := vaultURLIfPresent(SharedSecretsPath(opts.ProjectDir)); err != nil {
		return "", err
	} else if u != "" {
		return u, nil
	}
	return "", nil
}

func vaultURLIfPresent(path string) (string, error) {
	switch _, err := os.Stat(path); {
	case err == nil:
		return sopsio.VaultURL(path)
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	default:
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
}

// mergeSecretsFile decrypts path and merges its keys on top of out.
// A missing file is a no-op; SOPS errors propagate.
func mergeSecretsFile(path string, out map[string]string, layer string) error {
	switch _, err := os.Stat(path); {
	case err == nil:
		// fall through
	case errors.Is(err, fs.ErrNotExist):
		slog.Debug("lazurecfg: secrets file absent", "layer", layer, "path", path)
		return nil
	default:
		return fmt.Errorf("stat %s: %w", path, err)
	}

	slog.Debug("lazurecfg: decrypting secrets file", "layer", layer, "path", path)
	decrypted, err := sopsio.Decrypt(path)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", path, err)
	}
	maps.Copy(out, decrypted)
	slog.Debug("lazurecfg: secrets file merged", "layer", layer, "path", path, "count", len(decrypted))
	return nil
}
