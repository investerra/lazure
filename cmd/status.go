package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	return nil
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
