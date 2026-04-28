package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// StatusFlags returns the status-specific flag list for main.go to wire.
func StatusFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "format",
			Usage: "output format: table|json",
			Value: "table",
		},
	}
}

// Status implements `lazure status <env>`. Single GET of the deployed
// Container App; table or JSON output.
//
// Table shape:
//
//	App:         api-server
//	Env:         dev
//	Location:    switzerlandnorth
//	Ingress:     https://api-server.dev-env.switzerlandnorth.azurecontainerapps.io
//	Revision:    api-server--abc123
//	State:       Succeeded
//	Traffic:
//	  100% → latest
//	Replicas:    min=1 max=3
//
// JSON shape is the raw ARM body — callers can pipe to jq. Nothing is
// filtered; read-only fields are included because they're what you
// actually want to see in "status."
func Status(ctx context.Context, c *cli.Command) error {
	format := c.String("format")

	t, err := loadAzureTarget(c, "status")
	if err != nil {
		return err
	}
	slog.Debug("status: start", "env", t.Env, "dir", t.Dir, "format", format)

	slog.Debug("status: fetching container app", "subscription", t.Sub, "rg", t.RG, "app", t.Name)
	app, err := t.CA.Get(ctx, t.Sub, t.RG, t.Name)
	switch {
	case errors.Is(err, azureapi.ErrContainerAppNotFound):
		return errs.Usage(errs.Errorf("status: app %q not found in %s (not deployed yet?)", t.Name, t.RG))
	case err != nil:
		return errs.System(errs.Wrap(err, "status: get"))
	}

	switch format {
	case "", "table":
		return printStatusTable(t.Env, app)
	case "json":
		return printStatusJSON(app)
	default:
		return errs.Usage(errs.Errorf("status: invalid --format %q (want table|json)", format))
	}
}

// printStatusTable emits a compact human-oriented summary.
func printStatusTable(env string, app *azurearm.ContainerApp) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "App:\t%s\n", app.Name)
	fmt.Fprintf(tw, "Env:\t%s\n", env)
	fmt.Fprintf(tw, "Location:\t%s\n", app.Location)

	ing := app.Properties.Configuration.Ingress
	if ing != nil && ing.FQDN != "" {
		scheme := "https"
		if !ing.External && ing.AllowInsecure {
			scheme = "http"
		}
		fmt.Fprintf(tw, "Ingress:\t%s://%s\n", scheme, ing.FQDN)
	}

	if rev := app.Properties.LatestRevisionName; rev != "" {
		fmt.Fprintf(tw, "Revision:\t%s\n", rev)
	}
	if ps := app.Properties.ProvisioningState; ps != "" {
		fmt.Fprintf(tw, "State:\t%s\n", ps)
	}
	if mode := app.Properties.Configuration.ActiveRevisionsMode; mode != "" {
		fmt.Fprintf(tw, "RevisionsMode:\t%s\n", mode)
	}

	// Scale bounds only — current running replica count would need the
	// /revisions endpoint per revision; users wanting that detail run
	// `lazure revisions <env>` instead.
	if s := app.Properties.Template.Scale; s != nil {
		fmt.Fprintf(tw, "Replicas:\tmin=%d max=%d\n", s.MinReplicas, s.MaxReplicas)
	}
	if err := tw.Flush(); err != nil {
		return errs.System(errs.Wrap(err, "status: flush"))
	}

	// Traffic block — printed separately since it can be multi-line.
	if ing != nil && len(ing.Traffic) > 0 {
		fmt.Println("Traffic:")
		for _, t := range ing.Traffic {
			target := t.RevisionName
			if t.LatestRevision {
				target = "latest"
			}
			label := ""
			if t.Label != "" {
				label = fmt.Sprintf(" (%s)", t.Label)
			}
			fmt.Printf("  %3d%% → %s%s\n", t.Weight, target, label)
		}
	}

	if cs := app.Properties.Template.Containers; len(cs) > 0 {
		fmt.Println("Containers:")
		for _, c := range cs {
			printContainerSummary(c, "  ")
		}
	}
	if ics := app.Properties.Template.InitContainers; len(ics) > 0 {
		fmt.Println("Init containers:")
		for _, c := range ics {
			printContainerSummary(c, "  ")
		}
	}
	return nil
}

// printContainerSummary renders a per-container block for the status
// table. Indented with `prefix` to allow consistent rendering under
// either the Containers: or Init containers: header. Designed to
// answer "what's actually running in this app right now?":
//
//   - image (the load-bearing answer to most status questions)
//   - resource budget
//   - env-var surface — counts by kind so secret refs are visible
//     without dumping values
//   - probe summary (type + endpoint shape)
//   - mount points
//   - command/args overrides if set (image defaults are interesting
//     by their absence, but explicit overrides change behavior)
func printContainerSummary(c azurearm.Container, prefix string) {
	fmt.Printf("%s%s\n", prefix, displayName(c.Name))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	indent := prefix + "  "
	fmt.Fprintf(tw, "%sImage:\t%s\n", indent, fallbackString(c.Image, "—"))
	if r := c.Resources; r != nil {
		fmt.Fprintf(tw, "%sResources:\t%s\n", indent, formatResources(r))
	}
	if env := c.Env; len(env) > 0 {
		fmt.Fprintf(tw, "%sEnv:\t%s\n", indent, formatEnvCounts(env))
	}
	if mounts := c.VolumeMounts; len(mounts) > 0 {
		fmt.Fprintf(tw, "%sMounts:\t%s\n", indent, formatMounts(mounts))
	}
	if probes := c.Probes; len(probes) > 0 {
		fmt.Fprintf(tw, "%sProbes:\t%s\n", indent, formatProbes(probes))
	}
	if len(c.Command) > 0 {
		fmt.Fprintf(tw, "%sCommand:\t%s\n", indent, strings.Join(c.Command, " "))
	}
	if len(c.Args) > 0 {
		fmt.Fprintf(tw, "%sArgs:\t%s\n", indent, strings.Join(c.Args, " "))
	}
	if c.WorkingDir != "" {
		fmt.Fprintf(tw, "%sWorkingDir:\t%s\n", indent, c.WorkingDir)
	}
	_ = tw.Flush()
}

func displayName(name string) string {
	if name == "" {
		return "<unnamed>"
	}
	return name
}

func fallbackString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// formatResources collapses a Resources block into "0.5 CPU / 1Gi".
func formatResources(r *azurearm.Resources) string {
	cpu := fmt.Sprintf("%g", r.CPU)
	mem := r.Memory
	if mem == "" {
		mem = "—"
	}
	return cpu + " CPU / " + mem
}

// formatEnvCounts breaks env vars into "N plain, M secret refs" so
// callers see at a glance how many secrets are wired in. Values are
// never printed (would leak plaintext-config or, via secretRef, leak
// the Key Vault secret name to anyone reading status).
func formatEnvCounts(env []azurearm.EnvVar) string {
	plain, secret := 0, 0
	for _, e := range env {
		if e.SecretRef != "" {
			secret++
		} else {
			plain++
		}
	}
	switch {
	case plain > 0 && secret > 0:
		return fmt.Sprintf("%d plain, %d secret ref(s)", plain, secret)
	case secret > 0:
		return fmt.Sprintf("%d secret ref(s)", secret)
	case plain > 0:
		return fmt.Sprintf("%d plain", plain)
	default:
		return "—"
	}
}

// formatMounts renders volumes as "path (volumeName)[, path (volumeName)]".
func formatMounts(mounts []azurearm.VolumeMount) string {
	parts := make([]string, 0, len(mounts))
	for _, m := range mounts {
		parts = append(parts, fmt.Sprintf("%s (%s)", m.MountPath, m.VolumeName))
	}
	return strings.Join(parts, ", ")
}

// formatProbes summarises probes per type with endpoint + port. The
// probe slice can include up to three (Liveness, Readiness, Startup);
// rendering each as a short clause is more useful than a count.
func formatProbes(probes []azurearm.Probe) string {
	parts := make([]string, 0, len(probes))
	for _, p := range probes {
		var endpoint string
		switch {
		case p.HTTPGet != nil:
			endpoint = fmt.Sprintf("GET :%d%s", p.HTTPGet.Port, p.HTTPGet.Path)
		case p.TCPSocket != nil:
			endpoint = fmt.Sprintf("TCP :%d", p.TCPSocket.Port)
		default:
			endpoint = "(unknown action)"
		}
		typ := p.Type
		if typ == "" {
			typ = "Probe"
		}
		parts = append(parts, fmt.Sprintf("%s %s", typ, endpoint))
	}
	return strings.Join(parts, "; ")
}

// printStatusJSON emits the raw ARM body as pretty-printed JSON. We
// intentionally don't filter read-only fields here — the whole point
// of status is to see what Azure reports.
func printStatusJSON(app *azurearm.ContainerApp) error {
	out, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		return errs.System(errs.Wrap(err, "status: json marshal"))
	}
	fmt.Println(string(out))
	return nil
}
