package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
)

// Ports implements `lazure ports <env>`. Prints the deployed app's
// public ingress URL, FQDN, and target port — the bits users
// frequently look up in the Azure portal but already exist in the
// ARM body lazure GETs for `status`. Read-only; one ARM call.
//
// Output (table form):
//
//	URL:    https://api-server.greenmoss-c52e7df6.switzerlandnorth.azurecontainerapps.io
//	FQDN:   api-server.greenmoss-c52e7df6.switzerlandnorth.azurecontainerapps.io
//	Port:   8000
//	External: true
//
// When ingress is disabled or the app isn't deployed yet, prints
// "no ingress configured" and exits 0.
func Ports(ctx context.Context, c *cli.Command) error {
	t, err := loadAzureTarget(c, "ports")
	if err != nil {
		return err
	}
	slog.Debug("ports: fetching app", "app", t.Name, "env", t.Env)

	app, err := t.CA.Get(ctx, t.Sub, t.RG, t.Name)
	if err != nil {
		return errs.System(errs.Wrap(err, "ports: get"))
	}

	ing := app.Properties.Configuration.Ingress
	if ing == nil {
		fmt.Println("no ingress configured")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if ing.FQDN != "" {
		// ACA always serves https on 443 for external ingress; the
		// target_port is the in-container listen port, not what the
		// user types in a browser.
		fmt.Fprintf(tw, "URL:\thttps://%s\n", ing.FQDN)
		fmt.Fprintf(tw, "FQDN:\t%s\n", ing.FQDN)
	}
	if ing.TargetPort != 0 {
		fmt.Fprintf(tw, "Target port:\t%d\n", ing.TargetPort)
	}
	fmt.Fprintf(tw, "External:\t%t\n", ing.External)
	if ing.Transport != "" {
		fmt.Fprintf(tw, "Transport:\t%s\n", ing.Transport)
	}
	return tw.Flush()
}
