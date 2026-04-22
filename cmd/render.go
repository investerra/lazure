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
	env := c.StringArg("env")
	if env == "" {
		return fmt.Errorf("render: env argument is required (e.g. 'lazure render dev')")
	}
	dir := c.String("dir")

	manifest, vars, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{
		ProjectDir: dir,
		Env:        env,
	})
	if err != nil {
		return fmt.Errorf("render: loading manifest: %w", err)
	}

	result := lazurecfg.Validate(manifest)
	for _, w := range result.Warnings {
		slog.Warn(w)
	}
	if result.HasErrors() {
		return fmt.Errorf("render: %w", result.Err())
	}

	vaultURL, _ := vars["keyvault_url"].(string)

	arm, err := azurearm.Transform(manifest, azurearm.TransformOptions{
		VaultURL: vaultURL,
	})
	if err != nil {
		return fmt.Errorf("render: transform: %w", err)
	}

	out, err := yaml.Marshal(arm)
	if err != nil {
		return fmt.Errorf("render: marshal: %w", err)
	}
	fmt.Print(string(out))
	return nil
}
