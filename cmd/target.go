package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
)

// azureTarget gathers everything an env-taking command needs to talk
// to Azure: the loaded manifest, derived subscription/resource-group/
// app-name, and a ready-to-use ContainerApps client. Centralizing
// this preamble eliminates ~15 lines of boilerplate that used to sit
// at the top of every env-aware command.
type azureTarget struct {
	Env      string
	Dir      string
	Manifest *lazurecfg.Manifest
	Vars     map[string]any
	Sub      string
	SubName  string // display name from ARM, e.g. "Production"; empty if probe was skipped
	TenantID string // tenant of the subscription, useful in tenant-mismatch errors
	RG       string
	Name     string
	Tokens   *azureapi.TokenProvider
	CA       *azureapi.ContainerAppsClient
}

// SubLabel returns a friendly subscription identifier for confirm
// prompts and INFO logs. Format: "Production (12345…)" when the
// display name is known, just the GUID otherwise.
func (t *azureTarget) SubLabel() string {
	if t.SubName == "" {
		return t.Sub
	}
	return fmt.Sprintf("%s (%s)", t.SubName, shortenGUID(t.Sub))
}

// shortenGUID returns a 7-char prefix of a GUID for compact display.
// Mirrors `git rev-parse --short`. Full GUID stays available via t.Sub.
func shortenGUID(s string) string {
	if len(s) > 8 {
		return s[:8] + "…"
	}
	return s
}

// loadAzureTarget reads the env positional + --dir flag, loads the
// manifest, derives sub/rg/name from app.identity, and constructs a
// TokenProvider + ContainerAppsClient. Returns a fully-populated
// azureTarget or an appropriately-classified error (usage / auth /
// system) prefixed with cmdName so users see e.g. "deploy: load
// manifest: …" rather than a generic message.
//
// Probes the subscription up-front (one cheap ARM GET) so we can:
//   - display a friendly "Production (12345…)" in confirm prompts and
//     INFO logs instead of a bare GUID;
//   - turn the cryptic "401: invalid_token" that follows a tenant
//     mismatch into an actionable "you may need az login --tenant X"
//     before any business logic runs.
//
// Use this for commands that talk to ARM. Render-only commands that
// don't need a client should call loadManifestForCommand directly.
func loadAzureTarget(c *cli.Command, cmdName string) (*azureTarget, error) {
	m, vars, err := loadManifestForCommand(c, cmdName)
	if err != nil {
		return nil, err
	}
	sub := m.App.Identity.SubscriptionID()
	if sub == "" {
		return nil, errs.Usage(errs.Errorf("%s: could not derive subscription id from app.identity %q", cmdName, m.App.Identity))
	}
	tokens, err := azureapi.NewTokenProvider()
	if err != nil {
		return nil, errs.Auth(errs.Wrapf(err, "%s: auth", cmdName))
	}
	t := &azureTarget{
		Env:      c.StringArg("env"),
		Dir:      c.String("dir"),
		Manifest: m,
		Vars:     vars,
		Sub:      sub,
		RG:       m.App.ResourceGroup,
		Name:     m.App.Name,
		Tokens:   tokens,
		CA:       azureapi.NewContainerAppsClient(tokens),
	}
	if err := probeSubscription(context.Background(), t, cmdName); err != nil {
		return nil, err
	}
	return t, nil
}

// probeSubscription does the up-front sub lookup + classifies the
// usual failure modes into actionable errors. Mutates t.SubName +
// t.TenantID on success so downstream confirm prompts can format a
// friendly label.
//
// Failure shapes:
//   - 401 (auth) → tenant mismatch, suggest `az login --tenant`
//   - 403 (forbidden) → user lacks RBAC, suggest checking subscription
//     reader role
//   - 404 → wrong subscription id, suggest verifying app.identity
//   - other → wrap as system error with status detail
func probeSubscription(ctx context.Context, t *azureTarget, cmdName string) error {
	sub, err := azureapi.LookupSubscription(ctx, t.Tokens, t.Sub)
	switch {
	case err == nil:
		t.SubName = sub.DisplayName
		t.TenantID = sub.TenantID
		return nil
	case errors.Is(err, azureapi.ErrSubscriptionAuth):
		return errs.Auth(errs.Errorf(
			"%s: Azure rejected the token for subscription %s.\n"+
				"This usually means you're logged in to a different tenant. "+
				"Try: az login --tenant <correct-tenant-id>",
			cmdName, t.Sub))
	case errors.Is(err, azureapi.ErrSubscriptionForbidden):
		return errs.Auth(errs.Errorf(
			"%s: forbidden on subscription %s — your account doesn't have "+
				"Reader access. Ask your Azure admin to grant the role.",
			cmdName, t.Sub))
	case errors.Is(err, azureapi.ErrSubscriptionNotFound):
		return errs.Usage(errs.Errorf(
			"%s: subscription %s not found. Check that app.identity in "+
				"deploy.yml/vars.yml points at a real subscription id.",
			cmdName, t.Sub))
	default:
		return errs.System(errs.Wrapf(err, "%s: subscription probe", cmdName))
	}
}

// loadManifestForCommand handles the env-arg validation + LoadManifest
// call shared by every env-taking command. Splits out so render-only
// flows (render, diff) that don't need an Azure client can reuse the
// loading half without paying for token-provider construction.
//
// CLI --var overrides are honored when the command declares the
// --var slice flag (deploy/render/diff); other commands ignore them.
func loadManifestForCommand(c *cli.Command, cmdName string) (*lazurecfg.Manifest, map[string]any, error) {
	env := c.StringArg("env")
	if env == "" {
		return nil, nil, errs.Usage(errs.Errorf("%s: env argument is required (e.g. 'lazure %s dev')", cmdName, cmdName))
	}
	dir := c.String("dir")
	cliVars, _ := tryParseCLIVars(c)
	m, vars, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{
		ProjectDir: dir,
		Env:        env,
		CLIVars:    cliVars,
	})
	if err != nil {
		return nil, nil, errs.Usage(errs.Wrapf(err, "%s: load manifest", cmdName))
	}
	return m, vars, nil
}

// tryParseCLIVars extracts --var key=value flags if the command
// declares them, otherwise returns nil. Avoids forcing every command
// that calls loadManifestForCommand to declare --var even when it
// doesn't accept them. The error path is intentionally swallowed:
// if any --var is malformed the caller's flag parser would have
// errored already.
func tryParseCLIVars(c *cli.Command) (map[string]string, error) {
	raw := c.StringSlice("var")
	if len(raw) == 0 {
		return nil, nil
	}
	return parseCLIVars(raw)
}

// resolveLatestRevision GETs the current ContainerApp and returns its
// LatestRevisionName. Used by every command that takes an optional
// --revision flag and falls back to "current latest" when the user
// didn't pin a specific one (restart, rollback target picker, logs,
// exec, revisions). cmdName prefixes the error chain so users see
// which command's resolution step failed.
func resolveLatestRevision(ctx context.Context, t *azureTarget, cmdName string) (string, error) {
	app, err := t.CA.Get(ctx, t.Sub, t.RG, t.Name)
	switch {
	case errors.Is(err, azureapi.ErrContainerAppNotFound):
		return "", errs.Usage(errs.Errorf("%s: app %q not deployed yet", cmdName, t.Name))
	case err != nil:
		return "", errs.System(errs.Wrapf(err, "%s: fetch current state", cmdName))
	}
	rev := app.Properties.LatestRevisionName
	if rev == "" {
		return "", errs.System(errs.Errorf("%s: app has no latestRevisionName — is it still provisioning?", cmdName))
	}
	return rev, nil
}
