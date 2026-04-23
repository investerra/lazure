package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/cmd"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/logging"
)

// Version is injected at build time via -ldflags "-X main.Version=...".
var Version = "dev"

var errNotImplemented = errs.New("not implemented yet")

func main() {
	app := newApp()
	ctx := context.Background()
	if err := app.Run(ctx, os.Args); err != nil {
		// --log-level=debug prints the full %+v stack trace chain that
		// pkg/errors records; otherwise a one-line message.
		if slog.Default().Enabled(ctx, slog.LevelDebug) {
			slog.Error(fmt.Sprintf("%+v", err))
		} else {
			slog.Error(err.Error())
		}
		os.Exit(errs.Code(err))
	}
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:    "lazure",
		Usage:   "Deploy and manage Azure Container Apps",
		Version: Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "log-level",
				Usage:    "log verbosity: debug|info|warn|error",
				Value:    "info",
				Sources:  cli.EnvVars("LAZURE_LOG_LEVEL"),
				Category: "Global",
			},
			&cli.StringFlag{
				Name:     "log-format",
				Usage:    "log output format: text|json",
				Value:    "text",
				Sources:  cli.EnvVars("LAZURE_LOG_FORMAT"),
				Category: "Global",
			},
			&cli.StringFlag{
				Name:     "dir",
				Usage:    "project directory containing deploy.yml + envs/ (default: ./deploy)",
				Value:    "deploy",
				Sources:  cli.EnvVars("LAZURE_DIR"),
				Category: "Global",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if err := logging.Setup(cmd.String("log-level"), cmd.String("log-format")); err != nil {
				return ctx, err
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			// Deploy pipeline
			{
				Name:      "deploy",
				Usage:     "deploy to an environment",
				Arguments: envArg(),
				Flags:     cmd.DeployFlags(),
				Action:    cmd.Deploy,
			},
			{Name: "render", Usage: "print generated ARM YAML to stdout", Arguments: envArg(), Action: cmd.Render},
			{
				Name:      "diff",
				Usage:     "diff rendered template vs deployed app",
				Arguments: envArg(),
				Flags:     cmd.DiffFlags(),
				Action:    cmd.Diff,
			},
			{Name: "release", Usage: "cut a calver tag and push", Action: stub("release")},
			{Name: "self-update", Usage: "update the lazure binary from GitHub releases", Action: stub("self-update")},

			// Ops / day-two
			{
				Name:      "status",
				Usage:     "show current state of the deployed app",
				Arguments: envArg(),
				Flags:     cmd.StatusFlags(),
				Action:    cmd.Status,
			},
			{Name: "logs", Usage: "stream container logs", Arguments: envArg(), Action: stub("logs")},
			{
				Name:      "revisions",
				Usage:     "list recent revisions",
				Arguments: envArg(),
				Flags:     cmd.RevisionsFlags(),
				Action:    cmd.Revisions,
			},
			{
				Name:      "rollback",
				Usage:     "shift traffic to a previous revision",
				Arguments: envArg(),
				Flags:     cmd.RollbackFlags(),
				Action:    cmd.Rollback,
			},
			{Name: "restart", Usage: "restart a revision", Arguments: envArg(), Action: stub("restart")},
			{Name: "exec", Usage: "exec into a container (shells out to az)", Arguments: envArg(), Action: stub("exec")},
			{Name: "doctor", Usage: "preflight diagnostic checks", Action: stub("doctor")},

			// Onboarding
			{Name: "init", Usage: "scaffold a new lazure project in the current directory", Action: stub("init")},

			// Config surface
			cmd.SecretsCommand(),
			cmd.VarsCommand(),
		},
		EnableShellCompletion: true,
	}
}

func envArg() []cli.Argument {
	return []cli.Argument{
		&cli.StringArg{Name: "env", UsageText: "target environment (dev|uat|prod|...)"},
	}
}

func stub(name string) cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		return errs.System(errs.Wrapf(errNotImplemented, "%s", name))
	}
}
