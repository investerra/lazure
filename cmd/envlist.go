package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
)

// EnvList implements `lazure env list`. Prints the available
// environments under --dir, one per line, in alphabetical order. Same
// source of truth used by shell completion + doctor — anything with
// a corresponding `envs/<name>.vars.yml`.
//
// No Azure calls. Useful as a sanity check ("did init see all my
// envs?") and as a command-line input source for shell scripts:
//
//	for env in $(lazure env list); do lazure status "$env"; done
func EnvList(ctx context.Context, c *cli.Command) error {
	dir := c.String("dir")
	for _, env := range discoverEnvs(dir) {
		fmt.Fprintln(os.Stdout, env)
	}
	return nil
}
