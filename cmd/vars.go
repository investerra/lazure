package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
	"github.com/investerra/lazure/internal/verify"
)

// VarsCommand returns the `lazure vars` subcommand with its three
// actions: view, edit, verify.
//
// vars files are plaintext — no encryption, no SOPS, no Azure calls.
// This command is just convenience wrapping around lazurecfg.LoadVars
// + an $EDITOR launch + verify.Vars.
func VarsCommand() *cli.Command {
	return &cli.Command{
		Name:  "vars",
		Usage: "manage plain-text variables",
		Commands: []*cli.Command{
			{
				Name:      "view",
				Usage:     "view effective vars (std_vars + envs/{env}.vars.yml + CLI --var overrides)",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "format", Usage: "output format: table|json", Value: "table"},
					&cli.StringSliceFlag{Name: "var", Usage: "apply --var key=value overrides before showing (repeatable)"},
				},
				Action: VarsView,
			},
			{
				Name:      "edit",
				Usage:     "open envs/{env}.vars.yml in $EDITOR (creates the file if missing)",
				Arguments: envArgs(),
				Action:    VarsEdit,
			},
			{
				Name:      "verify",
				Usage:     "verify the manifest + vars load and validate without errors",
				Arguments: envArgs(),
				Action:    VarsVerify,
			},
		},
	}
}

// ---------- view ----------

// VarsView implements `lazure vars view <env>`.
func VarsView(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("vars view: env argument is required"))
	}
	dir := c.String("dir")
	format := c.String("format")
	cliVars, err := parseCLIVars(c.StringSlice("var"))
	if err != nil {
		return err
	}
	slog.Debug("vars view: start", "env", env, "dir", dir, "format", format, "cli_vars", len(cliVars))

	vars, err := lazurecfg.LoadVars(lazurecfg.LoadOptions{
		ProjectDir: dir, Env: env, CLIVars: cliVars,
	})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "vars view"))
	}

	switch format {
	case "", "table":
		return printVarsTable(env, vars)
	case "json":
		return printVarsJSON(vars)
	default:
		return errs.Usage(errs.Errorf("invalid --format %q (want table|json)", format))
	}
}

func printVarsTable(env string, vars map[string]any) error {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "# vars for %s\n", env)
	fmt.Fprintln(tw, "NAME\tVALUE")
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%v\n", k, vars[k])
	}
	return tw.Flush()
}

func printVarsJSON(vars map[string]any) error {
	// yaml.Marshal → YAMLToJSON gives us alphabetical keys (sigs.k8s.io
	// sorts via JSON). json.Marshal would do the same, but via yaml.Marshal
	// we avoid worrying about non-marshalable values like time.Time.
	y, err := yaml.Marshal(vars)
	if err != nil {
		return errs.System(errs.Wrap(err, "vars view: marshal"))
	}
	j, err := yaml.YAMLToJSON(y)
	if err != nil {
		return errs.System(errs.Wrap(err, "vars view: yaml->json"))
	}
	fmt.Println(string(j))
	return nil
}

// ---------- edit ----------

// VarsEdit implements `lazure vars edit <env>`. Opens the plaintext
// vars file in $EDITOR. If the file doesn't exist, creates a stub
// (so `lazure vars edit newenv` just works). After editing, attempts
// to load the file to catch YAML/template syntax errors and surface
// them before the user moves on.
func VarsEdit(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("vars edit: env argument is required"))
	}
	dir := c.String("dir")
	varsPath := filepath.Join(dir, "envs", env+".vars.yml")
	slog.Debug("vars edit: start", "env", env, "path", varsPath)

	editor := os.Getenv("EDITOR")
	if editor == "" {
		return errs.System(errs.New("$EDITOR not set; run e.g. 'export EDITOR=vim' and retry"))
	}

	// Create the file + its envs/ directory if missing.
	if _, err := os.Stat(varsPath); os.IsNotExist(err) {
		slog.Debug("vars edit: creating new vars file", "path", varsPath)
		if err := os.MkdirAll(filepath.Dir(varsPath), 0o755); err != nil {
			return errs.System(errs.Wrapf(err, "vars edit: mkdir %s", filepath.Dir(varsPath)))
		}
		stub := "# vars for " + env + " — plain text, no encryption\n"
		if err := os.WriteFile(varsPath, []byte(stub), 0o644); err != nil {
			return errs.System(errs.Wrapf(err, "vars edit: create %s", varsPath))
		}
	} else if err != nil {
		return errs.System(errs.Wrapf(err, "vars edit: stat %s", varsPath))
	}

	edit := exec.CommandContext(ctx, editor, varsPath)
	edit.Stdin, edit.Stdout, edit.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := edit.Run(); err != nil {
		return errs.System(errs.Wrap(err, "vars edit: $EDITOR exited with error"))
	}

	// Post-save syntax check — load the vars file so templating + YAML
	// parsing errors surface immediately. Cheap insurance against silent
	// typos that would only bite on next deploy.
	slog.Debug("vars edit: post-save syntax check")
	if _, err := lazurecfg.LoadVars(lazurecfg.LoadOptions{ProjectDir: dir, Env: env}); err != nil {
		return errs.Validation(errs.Wrap(err, "vars edit: saved file fails to render — fix the error and re-edit"))
	}

	slog.Info("vars saved", "env", env)
	return nil
}

// ---------- verify ----------

// VarsVerify implements `lazure vars verify <env>`. Loads the full
// manifest (which renders vars.yml + deploy.yml and runs structural
// validation via verify.Vars → lazurecfg.Validate).
func VarsVerify(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("vars verify: env argument is required"))
	}
	dir := c.String("dir")
	slog.Debug("vars verify: start", "env", env, "dir", dir)

	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{
		ProjectDir: dir, Env: env,
	})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "vars verify: load manifest"))
	}

	result := verify.Vars(manifest)
	for _, w := range result.Warnings {
		slog.Warn(w)
	}
	if result.HasErrors() {
		return errs.Validation(result.Err())
	}

	slog.Info("vars verified", "env", env)
	return nil
}
