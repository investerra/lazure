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
	print := c.Bool("print")
	yes := c.Bool("yes")
	wait := c.Bool("wait")
	waitTimeout := c.Duration("wait-timeout")
	streamLogs := c.Bool("logs")
	color := shouldColor(c.Bool("no-color"))

	t, err := loadAzureTarget(c, "deploy")
	if err != nil {
		return err
	}
	slog.Debug("deploy: start",
		"env", t.Env, "dir", t.Dir, "print", print, "yes", yes,
		"subscription", t.Sub, "resource_group", t.RG, "app", t.Name)

	// Validate before any side effects.
	if r := lazurecfg.Validate(t.Manifest); r.HasErrors() {
		return errs.Validation(errs.Wrap(r.Err(), "deploy"))
	}
	for _, w := range lazurecfg.Validate(t.Manifest).Warnings {
		slog.Warn(w)
	}

	// Cross-file secret reference check (no KV call — quick pre-flight).
	slog.Debug("deploy: checking secret references")
	encPath := filepath.Join(t.Dir, "envs", t.Env+".secrets.yml")
	secrets, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "deploy: decrypt secrets"))
	}
	if r := verify.Secrets(ctx, t.Manifest, secrets, nil); r.HasErrors() {
		return errs.Validation(errs.Wrap(r.Err(), "deploy"))
	}

	// Resolve previous revision. Missing app = first deploy.
	var previousRev string
	slog.Debug("deploy: fetching current state (for traffic.previous resolution)")
	current, err := t.CA.Get(ctx, t.Sub, t.RG, t.Name)
	switch {
	case errors.Is(err, azureapi.ErrContainerAppNotFound):
		slog.Info("first deploy detected; no previous revision to split traffic with", "app", t.Name)
	case err != nil:
		return errs.System(errs.Wrap(err, "deploy: fetch current app state"))
	default:
		previousRev = current.Properties.LatestRevisionName
		slog.Debug("deploy: current app state",
			"revision", previousRev,
			"provisioning_state", current.Properties.ProvisioningState)
	}

	// Transform to ARM.
	vaultURL, _ := t.Vars["keyvault_url"].(string)
	armApp, err := azurearm.Transform(t.Manifest, azurearm.TransformOptions{
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
	image := findAppImage(t.Manifest)
	if !yes {
		fmt.Printf("\ndeploy %s to %s\n  sub:   %s\n  rg:    %s\n  image: %s\n",
			t.Name, t.Env, t.SubLabel(), t.RG, image)
		if !promptConfirm("proceed?") {
			return errs.Usage(errs.New("deploy: aborted by user"))
		}
	}

	// PUT + poll.
	slog.Info("deploying", "app", t.Name, "env", t.Env, "sub", t.SubLabel(), "rg", t.RG, "image", image)
	start := time.Now()
	final, err := t.CA.PutAndWait(ctx, t.Sub, t.RG, t.Name, armApp)
	if err != nil {
		return errs.System(errs.Wrap(err, "deploy"))
	}

	slog.Info("deploy succeeded",
		"app", t.Name,
		"env", t.Env,
		"revision", final.Properties.LatestRevisionName,
		"duration", time.Since(start).Round(time.Second))

	if wait {
		newRev := final.Properties.LatestRevisionName
		if newRev == "" {
			slog.Warn("deploy: no latestRevisionName after PutAndWait — skipping --wait")
			return nil
		}
		if err := waitForRevisionReady(ctx, t.CA, t.Sub, t.RG, t.Name, newRev, waitTimeout, streamLogs, color); err != nil {
			return errs.System(errs.Wrap(err, "deploy: --wait"))
		}
		slog.Info("deploy --wait complete — all replicas Ready",
			"app", t.Name, "revision", newRev)
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
