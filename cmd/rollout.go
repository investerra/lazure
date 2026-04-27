package cmd

import (
	"context"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
)

// RolloutFlags returns the flags wired into the top-level `rollout`
// command in main.go.
func RolloutFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "no-build", Usage: "skip the build step (deploy a previously-pushed image)"},
		&cli.BoolFlag{Name: "pull", Usage: "always pull base images during build"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the deploy confirmation prompt"},
		&cli.StringSliceFlag{Name: "var", Usage: "extra --var KEY=VAL (repeatable, passed to deploy)"},
	}
}

// Rollout chains `build --push` + `deploy` so users don't have to
// retype the env three times. Implementation re-invokes the lazure
// binary as a subprocess — pragmatic v1, avoids cross-command
// state coupling. Global flags (--log-level, --log-format, --dir)
// propagate through preserveGlobals so child output stays
// consistent with the parent invocation.
func Rollout(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("env argument is required (e.g. 'lazure rollout dev')"))
	}

	self, err := os.Executable()
	if err != nil {
		return errs.System(errs.Wrap(err, "rollout: locate self"))
	}
	globals := preserveGlobals(c)

	if !c.Bool("no-build") {
		args := append([]string{}, globals...)
		args = append(args, "build", env, "--push")
		if c.Bool("pull") {
			args = append(args, "--pull")
		}
		slog.Info("rollout: build + push", "env", env)
		if err := runStreamed(ctx, self, args...); err != nil {
			return errs.System(errs.Wrap(err, "rollout: build/push"))
		}
	}

	args := append([]string{}, globals...)
	args = append(args, "deploy", env)
	if c.Bool("yes") {
		args = append(args, "-y")
	}
	for _, v := range c.StringSlice("var") {
		args = append(args, "--var", v)
	}
	slog.Info("rollout: deploy", "env", env)
	if err := runStreamed(ctx, self, args...); err != nil {
		return errs.System(errs.Wrap(err, "rollout: deploy"))
	}

	return nil
}

// preserveGlobals copies global flags from the parent invocation so
// re-invoked subcommands behave the same. --dir is only propagated
// when set to a non-default value to keep child argv tight.
func preserveGlobals(c *cli.Command) []string {
	var args []string
	if v := c.String("log-level"); v != "" {
		args = append(args, "--log-level", v)
	}
	if v := c.String("log-format"); v != "" {
		args = append(args, "--log-format", v)
	}
	if v := c.String("dir"); v != "" && v != "deploy" {
		args = append(args, "--dir", v)
	}
	return args
}
