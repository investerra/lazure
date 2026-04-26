package cmd

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
	"github.com/investerra/lazure/internal/sopsio"
	"github.com/investerra/lazure/internal/verify"
)

// Validate implements `lazure validate <env>` — a single-shot pre-flight
// that runs every static check lazure can do without talking to Azure:
//
//   - manifest renders (template + sops + std vars merge)
//   - structural validate (ScaleRule shape, Probe shape, etc.)
//   - cross-file secret refs match SOPS file
//   - ARM transform produces a payload (catches CollectSecretRefs +
//     identity expansion regressions)
//
// Cheaper alternative to `lazure render dev > /dev/null && lazure
// secrets verify dev` etc. Useful as a CI gate.
//
// Does NOT touch the network: no ARM calls, no Key Vault calls.
// `lazure secrets verify --check-kv` covers the optional online check.
func Validate(ctx context.Context, c *cli.Command) error {
	manifest, vars, err := loadManifestForCommand(c, "validate")
	if err != nil {
		return err
	}
	env := c.StringArg("env")
	dir := c.String("dir")
	slog.Debug("validate: start", "env", env, "dir", dir)

	// Structural validate: types, required fields, exclusivity rules.
	r := lazurecfg.Validate(manifest)
	for _, w := range r.Warnings {
		slog.Warn(w)
	}
	if r.HasErrors() {
		return errs.Validation(errs.Wrap(r.Err(), "validate"))
	}
	slog.Debug("validate: structural ok")

	// Cross-file secret refs. Decrypt is required to know which names
	// SOPS provides; we're not calling KV here.
	encPath := filepath.Join(dir, "envs", env+".secrets.yml")
	secrets, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "validate: decrypt secrets"))
	}
	if r := verify.Secrets(ctx, manifest, secrets, nil); r.HasErrors() {
		return errs.Validation(errs.Wrap(r.Err(), "validate"))
	}
	slog.Debug("validate: secret references ok")

	// Transform exercises CollectSecretRefs + identity expansion +
	// traffic resolution. PreviousRevision is intentionally empty —
	// validate doesn't talk to Azure.
	vaultURL, _ := vars["keyvault_url"].(string)
	if _, err := azurearm.Transform(manifest, azurearm.TransformOptions{VaultURL: vaultURL}); err != nil {
		return errs.System(errs.Wrap(err, "validate: transform"))
	}

	slog.Info("validate ok",
		"env", env,
		"app", manifest.App.Name,
		"containers", len(manifest.Containers),
		"secrets", len(secrets))
	return nil
}
