package cmd

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
)

// BuildFlags returns the flags wired into the top-level `build`
// command in main.go.
func BuildFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "push", Usage: "push the built image to its registry"},
		&cli.BoolFlag{Name: "pull", Usage: "always pull base images (passes --pull to docker build)"},
		&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "Dockerfile path (default: Dockerfile in build context)"},
		&cli.StringFlag{Name: "context", Usage: "build context (default: project root)"},
		&cli.StringSliceFlag{Name: "build-arg", Usage: "additional --build-arg KEY=VAL (repeatable)"},
		&cli.StringSliceFlag{Name: "secret", Usage: "additional --secret value passed verbatim to docker (repeatable)"},
	}
}

// Build implements `lazure build <env>`. Wraps `docker build` (and
// optionally `docker push` via `az acr login`) using the env's
// docker_image and acr_server vars as inputs. Auto-injects four
// build-args every project will likely consume: APP_VERSION,
// GIT_COMMIT, GIT_BRANCH, BUILD_DATE.
func Build(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("env argument is required (e.g. 'lazure build dev')"))
	}
	dir := c.String("dir")
	push := c.Bool("push")
	pull := c.Bool("pull")
	slog.Debug("build: start", "env", env, "dir", dir, "push", push, "pull", pull)

	vars, err := lazurecfg.LoadVars(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "build: load vars"))
	}
	return runImageBuild(ctx, imageBuildOptions{
		Env:        env,
		ProjectDir: dir,
		Vars:       vars,
		Push:       push,
		Pull:       pull,
		Dockerfile: c.String("file"),
		ContextDir: c.String("context"),
		BuildArgs:  c.StringSlice("build-arg"),
		Secrets:    c.StringSlice("secret"),
	})
}

type commandRunner func(context.Context, string, ...string) error
type pathLookup func(string) error

type imageBuildOptions struct {
	Env        string
	ProjectDir string
	Vars       map[string]any
	Push       bool
	Pull       bool
	Dockerfile string
	ContextDir string
	BuildArgs  []string
	Secrets    []string
	Runner     commandRunner
	Lookup     pathLookup
}

func runImageBuild(ctx context.Context, opts imageBuildOptions) error {
	runner := opts.Runner
	if runner == nil {
		runner = runStreamed
	}
	lookup := opts.Lookup
	if lookup == nil {
		lookup = func(name string) error {
			_, err := exec.LookPath(name)
			return err
		}
	}

	if err := lookup("docker"); err != nil {
		return errs.System(errs.New("build: 'docker' not found on PATH"))
	}
	if opts.Push {
		if err := lookup("az"); err != nil {
			return errs.System(errs.New("build: 'az' not found on PATH (required for --push to ACR)"))
		}
	}

	image, _ := opts.Vars["docker_image"].(string)
	if image == "" {
		return errs.Usage(errs.Errorf("build: docker_image var is required (set it in envs/%s.vars.yml)", opts.Env))
	}

	contextDir := opts.ContextDir
	if contextDir == "" {
		contextDir = filepath.Dir(filepath.Clean(opts.ProjectDir))
	}

	args := buildDockerArgs(image, contextDir, opts.Pull, opts.Dockerfile,
		autoBuildArgs(opts.Vars), opts.BuildArgs, opts.Secrets)

	slog.Info("docker build", "image", image, "context", contextDir)
	if err := runner(ctx, "docker", args...); err != nil {
		return errs.System(errs.Wrap(err, "build: docker build"))
	}

	if opts.Push {
		acrServer, _ := opts.Vars["acr_server"].(string)
		if acrServer == "" {
			return errs.Usage(errs.Errorf("build: acr_server var is required for --push (set it in envs/%s.vars.yml)", opts.Env))
		}
		acrName, ok := acrNameFromServer(acrServer)
		if !ok {
			return errs.Usage(errs.Errorf("build: acr_server %q is not a valid ACR login server (want <name>.azurecr.io)", acrServer))
		}
		slog.Info("az acr login", "registry", acrName)
		if err := runner(ctx, "az", "acr", "login", "--name", acrName); err != nil {
			return errs.System(errs.Wrap(err, "build: az acr login"))
		}
		slog.Info("docker push", "image", image)
		if err := runner(ctx, "docker", "push", image); err != nil {
			return errs.System(errs.Wrap(err, "build: docker push"))
		}
	}

	return nil
}

// buildDockerArgs assembles the argv passed to `docker build`. Pure
// for unit testing; no I/O.
//
// Order: --pull (if set), --file (if set), auto build args, user
// build args, secrets, -t <image>, <context>. Tag goes BEFORE
// context since context must be the final positional argument.
func buildDockerArgs(image, contextDir string, pull bool, dockerfile string, autoArgs, userArgs, secrets []string) []string {
	args := []string{"build"}
	if pull {
		args = append(args, "--pull")
	}
	if dockerfile != "" {
		args = append(args, "--file", dockerfile)
	}
	for _, ba := range autoArgs {
		args = append(args, "--build-arg", ba)
	}
	for _, ba := range userArgs {
		args = append(args, "--build-arg", ba)
	}
	for _, s := range secrets {
		args = append(args, "--secret", s)
	}
	args = append(args, "-t", image, contextDir)
	return args
}

// autoBuildArgs returns the four build-args lazure injects on every
// build. APP_VERSION is a duplicate of GIT_COMMIT so Dockerfiles
// that use either name work unchanged.
func autoBuildArgs(vars map[string]any) []string {
	out := make([]string, 0, 4)
	if commit, ok := vars["git_commit"].(string); ok && commit != "" {
		out = append(out, "GIT_COMMIT="+commit, "APP_VERSION="+commit)
	}
	if branch, ok := vars["git_branch"].(string); ok && branch != "" {
		out = append(out, "GIT_BRANCH="+branch)
	}
	out = append(out, "BUILD_DATE="+time.Now().UTC().Format(time.RFC3339))
	return out
}

// acrNameFromServer extracts the registry NAME from an ACR login
// server string ("foo.azurecr.io" → "foo"). Returns ok=false if the
// string doesn't look like an ACR server.
func acrNameFromServer(server string) (string, bool) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", false
	}
	name, rest, found := strings.Cut(server, ".")
	if !found || name == "" || !strings.Contains(rest, "azurecr") {
		return "", false
	}
	return name, true
}
