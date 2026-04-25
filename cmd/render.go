// Package cmd holds the handler functions for every lazure CLI command.
// They are thin adapters that pull flags/args off the urfave/cli Command,
// call into internal packages to do the work, and write output to stdout
// or surface errors.
package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
)

// Render implements `lazure render <env>`. It loads + renders deploy.yml,
// runs validation (surfacing warnings via slog but failing on errors),
// transforms to the ARM Container App shape, and writes the result as
// YAML to stdout.
//
// Read-only: no Azure auth, no network calls. PreviousRevision is empty
// — the output reflects a first-deploy shape (100% traffic to latest,
// no blue/green resolution). For diff or deploy, the actual revision
// gets filled in at call time.
func Render(ctx context.Context, c *cli.Command) error {
	manifest, vars, err := loadManifestForCommand(c, "render")
	if err != nil {
		return err
	}
	slog.Debug("render: manifest loaded",
		"app", manifest.App.Name,
		"containers", len(manifest.Containers),
		"init_containers", len(manifest.InitContainers))

	slog.Debug("render: validating")
	result := lazurecfg.Validate(manifest)
	for _, w := range result.Warnings {
		slog.Warn(w)
	}
	if result.HasErrors() {
		return errs.Validation(errs.Wrap(result.Err(), "render"))
	}
	slog.Debug("render: validation passed")

	vaultURL, _ := vars["keyvault_url"].(string)
	slog.Debug("render: transforming to ARM", "vault", vaultURL)
	arm, err := azurearm.Transform(manifest, azurearm.TransformOptions{
		VaultURL: vaultURL,
	})
	if err != nil {
		return errs.System(errs.Wrap(err, "render: transform"))
	}
	slog.Debug("render: ARM assembled, marshaling YAML")

	out, err := yaml.Marshal(arm)
	if err != nil {
		return errs.System(errs.Wrap(err, "render: marshal"))
	}
	fmt.Print(string(out))
	slog.Debug("render: done", "bytes", len(out))
	return nil
}
