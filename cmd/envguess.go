package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
)

func EnvGuessCommand() *cli.Command {
	return &cli.Command{
		Name:  "env-guess",
		Usage: "guess deployment environment from the current git ref",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "env", Usage: "explicit environment override (also read from INPUT_ENV)"},
			&cli.StringFlag{Name: "ref-type", Usage: "git ref type: branch|tag (also read from REF_TYPE or GITHUB_REF_TYPE)"},
			&cli.StringFlag{Name: "ref-name", Usage: "git ref name (also read from REF_NAME or GITHUB_REF_NAME)"},
			&cli.BoolFlag{Name: "github-output", Usage: "also append environment=<env> to $GITHUB_OUTPUT"},
		},
		Action: EnvGuess,
		Description: `Env-guess maps the current branch or tag to a Lazure environment.
It is intended for GitHub Actions workflows that need the same deployment target locally and in CI.

Mapping:
  main, master  -> uat
  dev           -> dev
  pre           -> pre
  uat           -> uat
  any tag       -> prd

Examples:
  lazure env-guess                         guess from GitHub env vars or local git
  lazure env-guess --ref-name main         print uat
  lazure env-guess --ref-type tag --ref-name v20260430.1
  lazure env-guess --github-output         also writes environment=<env> to $GITHUB_OUTPUT`,
	}
}

func EnvGuess(ctx context.Context, c *cli.Command) error {
	ref := envGuessRef{
		InputEnv: firstNonEmpty(c.String("env"), os.Getenv("INPUT_ENV")),
		RefType:  firstNonEmpty(c.String("ref-type"), os.Getenv("REF_TYPE"), os.Getenv("GITHUB_REF_TYPE")),
		RefName:  firstNonEmpty(c.String("ref-name"), os.Getenv("REF_NAME"), os.Getenv("GITHUB_REF_NAME")),
	}
	if ref.RefName == "" && ref.RefType == "" {
		ref = resolveLocalGitRef(ctx, ref)
	}
	env, err := guessEnvironment(ref)
	if err != nil {
		return errs.Usage(err)
	}
	fmt.Fprintln(os.Stdout, env)
	if c.Bool("github-output") {
		if err := writeGitHubEnvironmentOutput(env); err != nil {
			return errs.System(errs.Wrap(err, "env-guess: write GITHUB_OUTPUT"))
		}
	}
	return nil
}

type envGuessRef struct {
	InputEnv string
	RefType  string
	RefName  string
}

func guessEnvironment(ref envGuessRef) (string, error) {
	if ref.InputEnv != "" {
		return ref.InputEnv, nil
	}
	refType := strings.TrimSpace(ref.RefType)
	refName := normalizeRefName(ref.RefName)
	if refType == "tag" || strings.HasPrefix(strings.TrimSpace(ref.RefName), "refs/tags/") {
		return "prd", nil
	}
	switch refName {
	case "main", "master":
		return "uat", nil
	case "dev":
		return "dev", nil
	case "pre":
		return "pre", nil
	case "uat":
		return "uat", nil
	case "":
		return "", errs.New("unable to determine git ref; pass --ref-name/--ref-type or run inside a git repository")
	default:
		return "", errs.Errorf("unsupported branch %q", refName)
	}
}

func normalizeRefName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "refs/heads/")
	name = strings.TrimPrefix(name, "refs/tags/")
	return name
}

func resolveLocalGitRef(ctx context.Context, ref envGuessRef) envGuessRef {
	branch := gitOutput(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branch != "" {
		ref.RefType = "branch"
		ref.RefName = branch
		return ref
	}
	tag := gitOutput(ctx, "describe", "--tags", "--exact-match")
	if tag != "" {
		ref.RefType = "tag"
		ref.RefName = tag
	}
	return ref
}

func gitOutput(ctx context.Context, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeGitHubEnvironmentOutput(env string) error {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return errs.New("$GITHUB_OUTPUT is not set")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "environment=%s\n", env)
	return err
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
