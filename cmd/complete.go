package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

// CompleteEnvs is an urfave/cli ShellComplete function. When the shell
// asks for candidates on the positional <env> arg (e.g. user hits TAB
// after `lazure deploy `), this prints one env name per line to stdout.
//
// Source of truth: envs/*.vars.yml under --dir. Safe for shells to call
// — no Azure requests, no manifest rendering, just a directory glob.
// Errors are silent: completion shouldn't crash the shell on e.g. a
// missing `envs/` directory, so we just emit nothing.
func CompleteEnvs(ctx context.Context, c *cli.Command) {
	dir := c.String("dir")
	for _, env := range discoverEnvs(dir) {
		fmt.Println(env)
	}
}
