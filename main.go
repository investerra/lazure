package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"
)

// Version is injected at build time via -ldflags "-X main.Version=...".
var Version = "dev"

var errNotImplemented = errors.New("not implemented yet")

func main() {
	app := newApp()
	if err := app.Run(context.Background(), os.Args); err != nil {
		slog.Error(err.Error())
		os.Exit(exitCodeFor(err))
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
				Usage:    "project directory containing lazure.yml + envs/",
				Value:    ".",
				Sources:  cli.EnvVars("LAZURE_DIR"),
				Category: "Global",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if err := setupLogging(cmd.String("log-level"), cmd.String("log-format")); err != nil {
				return ctx, err
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			// Deploy pipeline
			{Name: "deploy", Usage: "deploy to an environment", Arguments: envArg(), Action: stub("deploy")},
			{Name: "render", Usage: "print generated ARM YAML to stdout", Arguments: envArg(), Action: stub("render")},
			{Name: "diff", Usage: "diff rendered template vs deployed app", Arguments: envArg(), Action: stub("diff")},
			{Name: "release", Usage: "cut a calver tag and push", Action: stub("release")},
			{Name: "self-update", Usage: "update the lazure binary from GitHub releases", Action: stub("self-update")},

			// Ops / day-two
			{Name: "status", Usage: "show current state of the deployed app", Arguments: envArg(), Action: stub("status")},
			{Name: "logs", Usage: "stream container logs", Arguments: envArg(), Action: stub("logs")},
			{Name: "revisions", Usage: "list recent revisions", Arguments: envArg(), Action: stub("revisions")},
			{Name: "rollback", Usage: "shift traffic to a previous revision", Arguments: envArg(), Action: stub("rollback")},
			{Name: "restart", Usage: "restart a revision", Arguments: envArg(), Action: stub("restart")},
			{Name: "exec", Usage: "exec into a container (shells out to az)", Arguments: envArg(), Action: stub("exec")},
			{Name: "doctor", Usage: "preflight diagnostic checks", Action: stub("doctor")},

			// Onboarding
			{Name: "init", Usage: "scaffold a new lazure project in the current directory", Action: stub("init")},

			// Config surface
			{
				Name:  "secrets",
				Usage: "manage encrypted secrets",
				Commands: []*cli.Command{
					{Name: "view", Usage: "view secrets (redacted by default)", Arguments: envArg(), Action: stub("secrets view")},
					{Name: "edit", Usage: "decrypt, edit, re-encrypt secrets", Arguments: envArg(), Action: stub("secrets edit")},
					{Name: "verify", Usage: "verify secret references + optional KV existence", Arguments: envArg(), Action: stub("secrets verify")},
					{Name: "sync", Usage: "sync secrets to Key Vault", Arguments: envArg(), Action: stub("secrets sync")},
				},
			},
			{
				Name:  "vars",
				Usage: "manage plain-text variables",
				Commands: []*cli.Command{
					{Name: "view", Usage: "view effective vars", Arguments: envArg(), Action: stub("vars view")},
					{Name: "edit", Usage: "open vars.yml in $EDITOR", Arguments: envArg(), Action: stub("vars edit")},
					{Name: "verify", Usage: "verify vars render and are non-empty", Arguments: envArg(), Action: stub("vars verify")},
				},
			},
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
		return fmt.Errorf("%s: %w", name, errNotImplemented)
	}
}

func setupLogging(level, format string) error {
	lvl, err := parseLogLevel(level)
	if err != nil {
		return err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch format {
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	case "text", "":
		h = slog.NewTextHandler(os.Stderr, opts)
	default:
		return fmt.Errorf("invalid log-format %q (want text|json)", format)
	}
	slog.SetDefault(slog.New(h))
	return nil
}

func parseLogLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log-level %q (want debug|info|warn|error)", s)
	}
}

func exitCodeFor(err error) int {
	if errors.Is(err, errNotImplemented) {
		return 2
	}
	return 1
}
