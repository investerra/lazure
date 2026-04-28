// Package verify runs cross-file consistency checks for a lazure
// project: the manifest's secret references against the SOPS file,
// optionally against live Azure Key Vault, and the vars file against
// the manifest's structural requirements.
//
// This is distinct from lazurecfg.Validate, which does STRUCTURAL
// checks on a single rendered manifest in isolation. verify crosses
// file boundaries and can consult external systems.
package verify

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
)

// Result holds the outcome of a verification pass: blocking errors +
// advisory warnings. Mirrors lazurecfg.ValidationResult for API symmetry.
type Result struct {
	Errors   []string
	Warnings []string
}

// HasErrors reports whether any errors were recorded.
func (r *Result) HasErrors() bool { return len(r.Errors) > 0 }

// Err produces a single multi-line error listing every finding. Returns
// nil when there are no errors.
func (r *Result) Err() error {
	if !r.HasErrors() {
		return nil
	}
	return errs.Errorf("verify failed (%d error(s)):\n  - %s",
		len(r.Errors), strings.Join(r.Errors, "\n  - "))
}

func (r *Result) addError(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *Result) addWarn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// KeyVault is the subset of azureapi.KeyVaultClient verify.Secrets uses.
// Declared here so tests can substitute a fake without importing the
// real client, and to document what the KV check actually needs.
type KeyVault interface {
	SecretExists(ctx context.Context, name string) (bool, error)
}

// Secrets cross-checks every {secret: X} reference in the manifest
// against the decrypted SOPS secrets map:
//
//   - Ref present in manifest but missing from SOPS → ERROR
//   - Key present in SOPS but unreferenced by manifest → WARN
//
// If kv is non-nil, additionally checks that each referenced secret
// exists in Key Vault (ErrSecretNotFound → error). Skipped if any
// earlier check already failed for the same name to avoid double-
// reporting the same missing secret.
func Secrets(ctx context.Context, manifest *lazurecfg.Manifest, sopsSecrets map[string]string, kv KeyVault) *Result {
	r := &Result{}
	if manifest == nil {
		r.addError("verify.Secrets: manifest is nil")
		return r
	}

	refs := lazurecfg.CollectSecretRefs(manifest)
	refSet := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		refSet[ref] = struct{}{}
	}
	slog.Debug("verify: collected refs", "refs", len(refs), "sops_keys", len(sopsSecrets))

	// (0) Every name in play — whether referenced by the manifest or
	// stored in SOPS — must satisfy Azure Key Vault's character class
	// (^[0-9a-zA-Z-]+$, ≤127 chars). Failing this guarantees a 400
	// BadParameter on `secrets sync` and a 400 ContainerAppSecret-
	// KeyVaultUrlInvalid (reported as a misleading "different cloud"
	// message) on `lazure deploy` — both far easier to debug at
	// validate time. Reported once per offending name even if the
	// name appears on both sides.
	checked := map[string]struct{}{}
	addNameCheck := func(name, origin string) {
		if _, dup := checked[name]; dup {
			return
		}
		checked[name] = struct{}{}
		if err := azureapi.ValidateSecretName(name); err != nil {
			r.addError("secret %q (%s) is invalid for Azure Key Vault — only alphanumeric and hyphens allowed (≤%d chars). Suggested rename: %q",
				name, origin, azureapi.SecretNameMaxLen, azureapi.SuggestSecretName(name))
		}
	}
	// Walk in deterministic order so the error list is stable across runs.
	sortedRefs := append([]string(nil), refs...)
	sort.Strings(sortedRefs)
	for _, ref := range sortedRefs {
		origin := "manifest reference"
		if _, inSOPS := sopsSecrets[ref]; inSOPS {
			origin = "manifest reference + SOPS key"
		}
		addNameCheck(ref, origin)
	}
	sopsKeys := make([]string, 0, len(sopsSecrets))
	for k := range sopsSecrets {
		sopsKeys = append(sopsKeys, k)
	}
	sort.Strings(sopsKeys)
	for _, k := range sopsKeys {
		addNameCheck(k, "SOPS key")
	}

	// (1) Every reference must be in the SOPS file.
	missingInSOPS := map[string]struct{}{}
	for _, ref := range refs {
		if _, ok := sopsSecrets[ref]; !ok {
			r.addError("secret %q referenced in manifest but missing from SOPS file", ref)
			missingInSOPS[ref] = struct{}{}
		}
	}
	slog.Debug("verify: SOPS ref check done", "missing", len(missingInSOPS))

	// (2) Every SOPS key should be referenced.
	unused := 0
	for name := range sopsSecrets {
		if _, used := refSet[name]; !used {
			r.addWarn("SOPS secret %q is not referenced anywhere in the manifest", name)
			unused++
		}
	}
	slog.Debug("verify: unused SOPS keys check done", "unused", unused)

	// (3) Optional: each referenced secret must be in Key Vault.
	if kv != nil {
		toCheck := len(refs) - len(missingInSOPS)
		slog.Debug("verify: checking refs against Key Vault", "count", toCheck)
		for _, ref := range refs {
			if _, missing := missingInSOPS[ref]; missing {
				continue
			}
			slog.Debug("verify: kv check", "secret", ref)
			exists, err := kv.SecretExists(ctx, ref)
			if err != nil {
				r.addError("secret %q: Key Vault check failed: %v", ref, err)
				continue
			}
			if !exists {
				r.addError("secret %q referenced and present in SOPS but missing from Key Vault (run 'lazure secrets sync')", ref)
			}
		}
		slog.Debug("verify: kv checks done")
	}

	return r
}

// Vars runs the structural manifest validation. It's a thin convenience
// wrapper that returns results in the verify package's own Result shape
// so CLI callers don't have to straddle two result types.
//
// The underlying check — run by lazurecfg.Validate — covers every
// var-related failure mode:
//   - empty 'value:' entries after rendering (resolves from an
//     unset .Vars.X)
//   - app.{name,location,...} required fields
//   - containers[].name / image missing
//   - etc.
//
// Callers that want structural + cross-file checks combined typically
// run verify.Vars then verify.Secrets and merge results.
func Vars(manifest *lazurecfg.Manifest) *Result {
	r := &Result{}
	v := lazurecfg.Validate(manifest)
	r.Errors = append(r.Errors, v.Errors...)
	r.Warnings = append(r.Warnings, v.Warnings...)
	return r
}
