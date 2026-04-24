package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/schema"
)

// SchemaArgs is the positional arg for `lazure schema`.
func SchemaArgs() []cli.Argument {
	return []cli.Argument{
		&cli.StringArg{
			Name:      "path",
			UsageText: "output path — `-` writes to stdout; omit to write to <dir>/deploy.schema.json",
		},
	}
}

// Schema implements `lazure schema [path]`. Writes the embedded JSON
// Schema for deploy.yml to disk (default: <dir>/deploy.schema.json) or
// stdout (when path is `-`).
//
// Typical uses:
//
//	lazure schema                          # refresh deploy/deploy.schema.json
//	lazure schema -                        # pipe to jq / validator
//	lazure schema ./custom.schema.json     # write to custom path
//
// The generated schema comes from the lazure binary at build time;
// upgrade the binary (self-update / reinstall) to pick up schema
// changes that ship with a newer lazure release.
func Schema(ctx context.Context, c *cli.Command) error {
	path := c.StringArg("path")
	dir := c.String("dir")
	slog.Debug("schema: start", "path", path, "dir", dir)

	// `-` is the conventional shell marker for stdout.
	if path == "-" {
		if _, err := os.Stdout.Write(schema.JSON); err != nil {
			return errs.System(errs.Wrap(err, "schema: write stdout"))
		}
		fmt.Fprintln(os.Stdout)
		return nil
	}

	// Default path sits beside the project's deploy.yml so a modeline
	// of `$schema=./deploy.schema.json` inside deploy.yml resolves
	// correctly without any further setup.
	if path == "" {
		path = filepath.Join(dir, "deploy.schema.json")
	}

	if err := os.WriteFile(path, withTrailingNewline(schema.JSON), 0o644); err != nil {
		return errs.System(errs.Wrap(err, "schema: write "+path))
	}
	slog.Info("wrote schema", "path", path, "bytes", len(schema.JSON))
	return nil
}

// withTrailingNewline ensures the on-disk schema ends with a single
// newline — standard tooling convention and prevents noisy diffs
// when editors auto-insert one.
func withTrailingNewline(b []byte) []byte {
	if len(b) == 0 || b[len(b)-1] == '\n' {
		return b
	}
	out := make([]byte, len(b)+1)
	copy(out, b)
	out[len(b)] = '\n'
	return out
}
