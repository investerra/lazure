package cmd

import (
	"context"
	"errors"

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
	RG       string
	Name     string
	Tokens   *azureapi.TokenProvider
	CA       *azureapi.ContainerAppsClient
}

// loadAzureTarget reads the env positional + --dir flag, loads the
// manifest, derives sub/rg/name from app.identity, and constructs a
// TokenProvider + ContainerAppsClient. Returns a fully-populated
// azureTarget or an appropriately-classified error (usage / auth /
// system) prefixed with cmdName so users see e.g. "deploy: load
// manifest: …" rather than a generic message.
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
	return &azureTarget{
		Env:      c.StringArg("env"),
		Dir:      c.String("dir"),
		Manifest: m,
		Vars:     vars,
		Sub:      sub,
		RG:       m.App.ResourceGroup,
		Name:     m.App.Name,
		Tokens:   tokens,
		CA:       azureapi.NewContainerAppsClient(tokens),
	}, nil
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
