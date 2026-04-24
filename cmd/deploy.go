package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
	"github.com/investerra/lazure/internal/sopsio"
	"github.com/investerra/lazure/internal/verify"
)

// DeployFlags are the deploy-specific CLI flags wired by main.go.
func DeployFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "print", Usage: "dump generated ARM YAML to stdout before confirming"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt"},
		&cli.StringSliceFlag{Name: "var", Usage: "override a vars entry (repeatable): key=value"},
		&cli.BoolFlag{Name: "wait", Usage: "after ARM succeeds, wait until the new revision's replicas are Ready"},
		&cli.DurationFlag{Name: "wait-timeout", Value: 5 * time.Minute, Usage: "max wait time (default: 5m)"},
		&cli.BoolFlag{Name: "logs", Value: true, Usage: "stream logs from the first ready replica during --wait (--logs=false to disable)"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors in streamed logs (also honored via NO_COLOR env)"},
	}
}

// Deploy implements `lazure deploy <env>`. Flow:
//
//  1. Load + render the manifest (vars + template + CLI --var overrides)
//  2. Structural validate + cross-file secret-reference check (no KV call)
//  3. Resolve previous revision: GET current app; on NotFound, treat as
//     first deploy (traffic.previous entries are dropped downstream)
//  4. Transform to ARM with the resolved previous revision
//  5. If --print, emit the ARM YAML to stdout
//  6. Confirm deploy (skip with -y)
//  7. PutAndWait (poll Azure-AsyncOperation to Succeeded)
//  8. Report the final revision name
func Deploy(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("deploy: env argument is required (e.g. 'lazure deploy dev')"))
	}
	dir := c.String("dir")
	print := c.Bool("print")
	yes := c.Bool("yes")
	wait := c.Bool("wait")
	waitTimeout := c.Duration("wait-timeout")
	streamLogs := c.Bool("logs")
	color := shouldColor(c.Bool("no-color"))

	cliVars, err := parseCLIVars(c.StringSlice("var"))
	if err != nil {
		return err
	}
	slog.Debug("deploy: start", "env", env, "dir", dir, "print", print, "yes", yes, "cli_vars", len(cliVars))

	// Load + validate manifest.
	slog.Debug("deploy: loading manifest")
	manifest, vars, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{
		ProjectDir: dir,
		Env:        env,
		CLIVars:    cliVars,
	})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "deploy: load manifest"))
	}
	if r := lazurecfg.Validate(manifest); r.HasErrors() {
		return errs.Validation(errs.Wrap(r.Err(), "deploy"))
	}
	for _, w := range lazurecfg.Validate(manifest).Warnings {
		slog.Warn(w)
	}

	// Cross-file secret reference check (no KV call — quick pre-flight).
	slog.Debug("deploy: checking secret references")
	encPath := filepath.Join(dir, "envs", env+".secrets.yml")
	secrets, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "deploy: decrypt secrets"))
	}
	if r := verify.Secrets(ctx, manifest, secrets, nil); r.HasErrors() {
		return errs.Validation(errs.Wrap(r.Err(), "deploy"))
	}

	// Azure credential + container apps client.
	slog.Debug("deploy: creating Azure credential")
	tokens, err := azureapi.NewTokenProvider()
	if err != nil {
		return errs.Auth(errs.Wrap(err, "deploy: auth"))
	}
	ca := azureapi.NewContainerAppsClient(tokens)

	// Derive subscription from app.identity.
	sub := manifest.App.Identity.SubscriptionID()
	if sub == "" {
		return errs.Usage(errs.Errorf("deploy: could not derive subscription id from app.identity %q", manifest.App.Identity))
	}
	rg := manifest.App.ResourceGroup
	name := manifest.App.Name
	slog.Debug("deploy: resolved target", "subscription", sub, "resource_group", rg, "app", name)

	// Resolve previous revision. Missing app = first deploy.
	var previousRev string
	slog.Debug("deploy: fetching current state (for traffic.previous resolution)")
	current, err := ca.Get(ctx, sub, rg, name)
	switch {
	case errors.Is(err, azureapi.ErrContainerAppNotFound):
		slog.Info("first deploy detected; no previous revision to split traffic with", "app", name)
	case err != nil:
		return errs.System(errs.Wrap(err, "deploy: fetch current app state"))
	default:
		previousRev = current.Properties.LatestRevisionName
		slog.Debug("deploy: current app state",
			"revision", previousRev,
			"provisioning_state", current.Properties.ProvisioningState)
	}

	// Transform to ARM.
	vaultURL, _ := vars["keyvault_url"].(string)
	armApp, err := azurearm.Transform(manifest, azurearm.TransformOptions{
		VaultURL:         vaultURL,
		PreviousRevision: previousRev,
	})
	if err != nil {
		return errs.System(errs.Wrap(err, "deploy: transform"))
	}

	if print {
		out, err := yaml.Marshal(armApp)
		if err != nil {
			return errs.System(errs.Wrap(err, "deploy: marshal ARM"))
		}
		fmt.Println("--- rendered ARM payload ---")
		fmt.Println(string(out))
	}

	// Confirm.
	image := findAppImage(manifest)
	if !yes {
		fmt.Printf("\ndeploy %s to %s (rg=%s, image=%s)\n", name, env, rg, image)
		if !promptConfirm("proceed?") {
			return errs.Usage(errs.New("deploy: aborted by user"))
		}
	}

	// PUT + poll.
	slog.Info("deploying", "app", name, "env", env, "rg", rg, "image", image)
	start := time.Now()
	final, err := ca.PutAndWait(ctx, sub, rg, name, armApp)
	if err != nil {
		return errs.System(errs.Wrap(err, "deploy"))
	}

	slog.Info("deploy succeeded",
		"app", name,
		"env", env,
		"revision", final.Properties.LatestRevisionName,
		"duration", time.Since(start).Round(time.Second))

	if wait {
		newRev := final.Properties.LatestRevisionName
		if newRev == "" {
			slog.Warn("deploy: no latestRevisionName after PutAndWait — skipping --wait")
			return nil
		}
		if err := waitForRevisionReady(ctx, ca, sub, rg, name, newRev, waitTimeout, streamLogs, color); err != nil {
			return errs.System(errs.Wrap(err, "deploy: --wait"))
		}
		slog.Info("deploy --wait complete — all replicas Ready",
			"app", name, "revision", newRev)
	}
	return nil
}

// ---------- helpers ----------

// parseCLIVars converts repeated --var key=value flags into a map. Splits
// on the FIRST '=' so values may contain '=' characters (e.g. base64 blobs,
// connection strings).
func parseCLIVars(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, pair := range raw {
		idx := strings.Index(pair, "=")
		if idx < 0 {
			return nil, errs.Usage(errs.Errorf("invalid --var %q (want key=value)", pair))
		}
		key := pair[:idx]
		if key == "" {
			return nil, errs.Usage(errs.Errorf("invalid --var %q (empty key)", pair))
		}
		out[key] = pair[idx+1:]
	}
	return out, nil
}

// findAppImage returns the image of the container named "app" if present,
// else the first runtime container's image. Used only for confirm-prompt
// display — not deploy logic.
func findAppImage(m *lazurecfg.Manifest) string {
	for _, c := range m.Containers {
		if c.Name == "app" {
			return c.Image
		}
	}
	if len(m.Containers) > 0 {
		return m.Containers[0].Image
	}
	return "<no containers>"
}
