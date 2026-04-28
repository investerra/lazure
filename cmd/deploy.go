package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
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
		&cli.BoolFlag{Name: "force", Usage: "force a new revision by injecting a timestamp env var"},
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
	force := c.Bool("force")
	// --wait defaults to TRUE on interactive terminals (humans want to
	// see the deploy land), FALSE when piped (CI / scripts assume
	// fire-and-return semantics from years of `kubectl apply`-style
	// tooling). Users can override either way: --wait=false to opt
	// out on a TTY, --wait to opt in when piping.
	wait := c.Bool("wait")
	if !c.IsSet("wait") && isStdoutTTY() {
		wait = true
	}
	waitTimeout := c.Duration("wait-timeout")
	streamLogs := c.Bool("logs")
	color := shouldColor(c.Bool("no-color"))

	t, err := loadAzureTarget(c, "deploy")
	if err != nil {
		return err
	}
	slog.Debug("deploy: start",
		"env", t.Env, "dir", t.Dir, "print", print, "yes", yes,
		"force", force, "subscription", t.Sub, "resource_group", t.RG, "app", t.Name)

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
	var (
		previousRev          string
		previousImage        string
		previousProvisioning string
	)
	slog.Debug("deploy: fetching current state (for traffic.previous resolution)")
	current, err := t.CA.Get(ctx, t.Sub, t.RG, t.Name)
	switch {
	case errors.Is(err, azureapi.ErrContainerAppNotFound):
		slog.Info("first deploy detected; no previous revision to split traffic with", "app", t.Name)
	case err != nil:
		return errs.System(errs.Wrap(err, "deploy: fetch current app state"))
	default:
		previousRev = current.Properties.LatestRevisionName
		previousImage = firstContainerImage(current)
		previousProvisioning = current.Properties.ProvisioningState
		slog.Info("current state",
			"revision", previousRev,
			"image", previousImage,
			"provisioning_state", previousProvisioning)
		if previousProvisioning == "Failed" {
			slog.Warn("previous deploy left the app in Failed provisioning state — the upcoming PUT will attempt to overwrite it, but check `lazure events "+t.Env+"` if the new revision also fails",
				"revision", previousRev)
		}
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
	if force {
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		applyForceRedeployTimestamp(armApp, ts)
		slog.Info("force redeploy enabled", "env", forceRedeployEnvName, "value", ts)
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

	// PUT + poll. Indefinite spinner during the ARM async-op poll —
	// without it the user sees "deploying" then ~30-60s of silence.
	slog.Info("deploying", "app", t.Name, "env", t.Env, "sub", t.SubLabel(), "rg", t.RG, "image", image)
	start := time.Now()
	sp := newWaitSpinner(time.Time{})
	sp.SetMessage("ARM operation in progress")
	sp.Start()
	final, err := t.CA.PutAndWait(ctx, t.Sub, t.RG, t.Name, armApp)
	sp.Stop()
	if err != nil {
		return errs.System(errs.Wrap(err, "deploy"))
	}

	newRev := final.Properties.LatestRevisionName
	noNewRevision := previousRev != "" && newRev == previousRev
	duration := time.Since(start).Round(time.Second)

	logArgs := []any{
		"app", t.Name,
		"env", t.Env,
		"image", image,
		"duration", duration,
	}
	switch {
	case previousRev == "":
		logArgs = append(logArgs, "revision", newRev, "first_deploy", true)
	case noNewRevision:
		logArgs = append(logArgs, "revision", newRev, "new_revision_created", false)
	default:
		logArgs = append(logArgs, "previous_revision", previousRev, "new_revision", newRev)
	}
	slog.Info("deploy succeeded", logArgs...)

	if final.Properties.ProvisioningState != "" && final.Properties.ProvisioningState != "Succeeded" {
		slog.Warn("deploy: ARM provisioning state is not Succeeded — Azure accepted the PUT but the resource didn't reach a clean state. Check `lazure events "+t.Env+"` and revision-level details.",
			"provisioning_state", final.Properties.ProvisioningState,
			"running_status", final.Properties.RunningStatus,
			"revision", newRev)
	}

	if wait {
		if newRev == "" {
			slog.Warn("deploy: no latestRevisionName after PutAndWait — skipping --wait")
			return printDeployRecap(deployRecap{
				app: t.Name, env: t.Env, sub: t.SubLabel(), rg: t.RG,
				prevRevision: previousRev, newRevision: newRev,
				prevImage: previousImage, newImage: image,
				latestReady:       final.Properties.LatestReadyRevisionName,
				provisioningState: final.Properties.ProvisioningState,
				runningStatus:     final.Properties.RunningStatus,
				ingressURL:        ingressURL(final),
				duration:          duration,
				noNewRevision:     noNewRevision,
				firstDeploy:       previousRev == "",
			})
		}
		if err := waitForRevisionReady(ctx, t.CA, t.Sub, t.RG, t.Name, newRev, waitTimeout, streamLogs, color); err != nil {
			return errs.System(errs.Wrap(err, "deploy: --wait"))
		}
		slog.Info("deploy --wait complete — all replicas Ready",
			"app", t.Name, "revision", newRev)

		// Re-fetch so the recap reflects the post-wait state (e.g.
		// LatestReadyRevisionName advancing once replicas come up).
		if updated, err := t.CA.Get(ctx, t.Sub, t.RG, t.Name); err == nil {
			final = updated
		} else {
			slog.Debug("deploy: post-wait re-fetch failed (recap will use pre-wait state)", "err", err)
		}
	}

	return printDeployRecap(deployRecap{
		app: t.Name, env: t.Env, sub: t.SubLabel(), rg: t.RG,
		prevRevision:      previousRev,
		newRevision:       final.Properties.LatestRevisionName,
		prevImage:         previousImage,
		newImage:          image,
		latestReady:       final.Properties.LatestReadyRevisionName,
		provisioningState: final.Properties.ProvisioningState,
		runningStatus:     final.Properties.RunningStatus,
		ingressURL:        ingressURL(final),
		duration:          duration,
		noNewRevision:     previousRev != "" && final.Properties.LatestRevisionName == previousRev,
		firstDeploy:       previousRev == "",
	})
}

// ---------- recap ----------

// deployRecap is the information set we render at the end of `lazure
// deploy` so the user gets a single, scannable summary. Pure data —
// the renderer is responsible for cosmetics.
type deployRecap struct {
	app, env, sub, rg                string
	prevRevision, newRevision        string
	latestReady                      string
	prevImage, newImage              string
	provisioningState, runningStatus string
	ingressURL                       string
	duration                         time.Duration
	noNewRevision                    bool
	firstDeploy                      bool
}

func printDeployRecap(r deployRecap) error {
	fmt.Println()
	fmt.Println("──── deploy summary ────")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "App:\t%s\n", r.app)
	fmt.Fprintf(tw, "Env:\t%s  (sub: %s · rg: %s)\n", r.env, r.sub, r.rg)

	switch {
	case r.firstDeploy:
		fmt.Fprintf(tw, "Revision:\t%s  (first deploy)\n", fallbackString(r.newRevision, "—"))
	case r.noNewRevision:
		fmt.Fprintf(tw, "Revision:\t%s  (no new revision — Azure detected no template-level changes)\n", r.newRevision)
	default:
		fmt.Fprintf(tw, "Revision:\t%s → %s\n", fallbackString(r.prevRevision, "—"), fallbackString(r.newRevision, "—"))
	}

	if r.latestReady != "" && r.latestReady != r.newRevision {
		fmt.Fprintf(tw, "Ready revision:\t%s  (older — new revision not yet Ready)\n", r.latestReady)
	}

	if r.prevImage != "" && r.prevImage != r.newImage {
		fmt.Fprintf(tw, "Image:\t%s\n\t  → %s\n", r.prevImage, r.newImage)
	} else {
		fmt.Fprintf(tw, "Image:\t%s\n", fallbackString(r.newImage, "—"))
	}

	if r.provisioningState != "" || r.runningStatus != "" {
		state := r.provisioningState
		if r.runningStatus != "" {
			if state != "" {
				state += " / "
			}
			state += r.runningStatus
		}
		fmt.Fprintf(tw, "State:\t%s\n", state)
	}
	if r.ingressURL != "" {
		fmt.Fprintf(tw, "Ingress:\t%s\n", r.ingressURL)
	}
	fmt.Fprintf(tw, "Duration:\t%s\n", r.duration)
	if err := tw.Flush(); err != nil {
		return errs.System(errs.Wrap(err, "deploy: recap flush"))
	}
	fmt.Println()
	return nil
}

// firstContainerImage returns the image of the first non-init container,
// or "" if the app has none. Used by the deploy recap to compare the
// previously-deployed image against the one we're shipping.
func firstContainerImage(app *azurearm.ContainerApp) string {
	if app == nil {
		return ""
	}
	for _, c := range app.Properties.Template.Containers {
		if c.Image != "" {
			return c.Image
		}
	}
	return ""
}

// ingressURL formats the FQDN from a container app GET response into
// a fully-qualified URL, picking https/http based on whether ingress
// is external or insecure-allowed. Returns "" when there's no ingress.
func ingressURL(app *azurearm.ContainerApp) string {
	if app == nil {
		return ""
	}
	ing := app.Properties.Configuration.Ingress
	if ing == nil || ing.FQDN == "" {
		return ""
	}
	scheme := "https"
	if !ing.External && ing.AllowInsecure {
		scheme = "http"
	}
	return scheme + "://" + ing.FQDN
}

// ---------- helpers ----------

const forceRedeployEnvName = "LAZURE_FORCE_REDEPLOYED_AT"

func applyForceRedeployTimestamp(app *azurearm.ContainerApp, timestamp string) {
	if app == nil {
		return
	}
	for i := range app.Properties.Template.Containers {
		app.Properties.Template.Containers[i].Env = upsertPlainEnv(
			app.Properties.Template.Containers[i].Env,
			forceRedeployEnvName,
			timestamp,
		)
	}
}

func upsertPlainEnv(env []azurearm.EnvVar, name, value string) []azurearm.EnvVar {
	for i := range env {
		if env[i].Name == name {
			env[i].Value = value
			env[i].SecretRef = ""
			return env
		}
	}
	return append(env, azurearm.EnvVar{Name: name, Value: value})
}

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
