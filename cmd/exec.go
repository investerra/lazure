package cmd

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
)

// ExecFlags are the flags for `lazure exec`.
//
// Note on cmd quoting: tokens after `--` are joined with spaces and
// passed to `az --command`. Shell re-quoting is lost — e.g.
// `lazure exec dev -- echo "hello world"` runs `echo hello world`
// (three tokens server-side), not `echo "hello world"`. This is the
// same limitation az has with --command from the CLI.
func ExecFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "container", Usage: "container name (default: first defined container in the manifest)"},
		&cli.StringFlag{Name: "revision", Usage: "target revision (default: current latestRevisionName)"},
	}
}

// ExecArgs are the positional args for `lazure exec`. Everything after
// the conventional `--` separator is gathered into the cmd slice. urfave
// handles `--` natively — it stops flag parsing, so tokens past it land
// in this positional slice regardless of whether they look like flags.
//
// Max: -1 makes the cmd slice truly variadic; the default Max is 0 which
// rejects any tail args with "args cmd has max 0, not parsing argument".
func ExecArgs() []cli.Argument {
	return []cli.Argument{
		&cli.StringArg{Name: "env", UsageText: "target environment (dev|uat|prod|...)"},
		&cli.StringArgs{
			Name:      "cmd",
			UsageText: "command to run (prefix with -- to pass flags like -la)",
			Min:       0,
			Max:       -1,
		},
	}
}

// Exec implements `lazure exec <env> [--container X] -- <cmd...>`.
// Shells out to `az containerapp exec` because it's the only way to get
// a proper PTY + signal forwarding without reimplementing ACA's exec
// websocket client. The `az` binary dependency is scoped to THIS
// command — every other lazure feature uses the ARM REST API directly.
//
// Without cmd args, az defaults to `sh`, giving an interactive shell.
// With cmd args, they're joined with spaces and passed as --command;
// az shlex-tokenizes server-side, so `lazure exec dev -- ls -la /srv`
// runs `ls -la /srv` correctly. Quoting compound args (embedded
// spaces) is lossy — a limitation inherited from az itself.
func Exec(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("exec: env argument is required"))
	}
	container := c.String("container")
	rev := c.String("revision")
	cmdArgs := c.StringArgs("cmd")
	dir := c.String("dir")
	slog.Debug("exec: start",
		"env", env, "container", container, "revision", rev, "cmd", cmdArgs)

	if _, err := exec.LookPath("az"); err != nil {
		return errs.Usage(errs.New("exec: az binary not found on PATH — install Azure CLI (https://aka.ms/InstallAzureCli)"))
	}

	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "exec: load manifest"))
	}
	if container == "" {
		// lazurecfg/validate.go rejects 0-container manifests at load,
		// so manifest.Containers[0] is safe here.
		container = manifest.Containers[0].Name
		slog.Debug("exec: defaulting to first manifest container", "container", container)
	}

	sub := manifest.App.Identity.SubscriptionID()
	if sub == "" {
		return errs.Usage(errs.Errorf("exec: could not derive subscription id from app.identity %q", manifest.App.Identity))
	}
	rg, name := manifest.App.ResourceGroup, manifest.App.Name

	if rev == "" {
		tokens, err := azureapi.NewTokenProvider()
		if err != nil {
			return errs.Auth(errs.Wrap(err, "exec: auth"))
		}
		ca := azureapi.NewContainerAppsClient(tokens)
		app, err := ca.Get(ctx, sub, rg, name)
		switch {
		case errors.Is(err, azureapi.ErrContainerAppNotFound):
			return errs.Usage(errs.Errorf("exec: app %q not deployed yet", name))
		case err != nil:
			return errs.System(errs.Wrap(err, "exec: fetch current state"))
		}
		rev = app.Properties.LatestRevisionName
		if rev == "" {
			return errs.System(errs.New("exec: app has no latestRevisionName — is it still provisioning?"))
		}
		slog.Debug("exec: resolved revision", "revision", rev)
	}

	azArgs := buildAzExecArgs(name, rg, rev, container, cmdArgs)
	slog.Info("exec: launching az containerapp exec",
		"app", name, "revision", rev, "container", container, "cmd", cmdArgs)
	slog.Debug("exec: az args", "args", azArgs)

	az := exec.CommandContext(ctx, "az", azArgs...)
	az.Stdin = os.Stdin
	az.Stdout = os.Stdout
	az.Stderr = os.Stderr
	if err := az.Run(); err != nil {
		// az already wrote its own diagnostics to inherited stderr, so
		// an additional lazure-prefixed line would just be noise.
		// Propagate the exit code silently. Non-ExitError (e.g. failed
		// to spawn) still goes through the normal wrapped path.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return errs.Silent(ee.ExitCode(), err)
		}
		return errs.System(errs.Wrap(err, "exec: az containerapp exec"))
	}
	return nil
}

// buildAzExecArgs constructs the argv for `az containerapp exec`. Pure
// function — no side effects — so it's easy to verify the flag order
// and --command join behavior without invoking a real subprocess.
//
// When cmdArgs is empty, --command is omitted entirely and az falls
// back to its default (`sh`), giving an interactive shell.
func buildAzExecArgs(name, rg, rev, container string, cmdArgs []string) []string {
	out := []string{
		"containerapp", "exec",
		"--name", name,
		"--resource-group", rg,
		"--revision", rev,
		"--container", container,
	}
	if len(cmdArgs) > 0 {
		out = append(out, "--command", strings.Join(cmdArgs, " "))
	}
	return out
}
