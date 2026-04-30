package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/cmd"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/logging"
)

// Drop the default `-v` alias from urfave/cli's auto-injected
// --version flag — we want `-v` reserved for our own --verbose
// shortcut. With the default in place, both flags are registered with
// the same alias and the parser routes `--verbose` (and `-v`) to
// --version, printing the version string instead of enabling debug
// logging. This must run before newApp() builds the command.
func init() {
	cli.VersionFlag = &cli.BoolFlag{
		Name:        "version",
		Usage:       "print the version",
		HideDefault: true,
		Local:       true,
	}
}

// Build-time injection via -ldflags "-X main.Version=... -X main.Commit=... -X main.Date=...".
// Commit + Date are stamped by goreleaser; running `go build` without
// ldflags leaves them empty and the version line falls back to just
// "dev".
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// versionString is what `lazure --version` prints. Includes commit +
// date when stamped (release builds), bare version otherwise (go build
// during development).
func versionString() string {
	if Commit == "" && Date == "" {
		return Version
	}
	parts := []string{Version}
	if Commit != "" {
		short := Commit
		if len(short) > 7 {
			short = short[:7]
		}
		parts = append(parts, fmt.Sprintf("commit %s", short))
	}
	if Date != "" {
		parts = append(parts, Date)
	}
	return fmt.Sprintf("%s (%s)", parts[0], strings.Join(parts[1:], ", "))
}

func main() {
	// Register this package's build-time Version with the selfupdate
	// command so it can compare against GitHub Releases.
	cmd.SetVersionGetter(func() string { return Version })
	app := newApp()
	ctx := context.Background()
	if err := app.Run(ctx, os.Args); err != nil {
		// Silent errors come from subprocess wrappers where the child
		// already wrote its own diagnostics to inherited stderr; adding
		// a lazure-prefixed line on top would be noise.
		if !errs.IsSilent(err) {
			slog.Error(topLevelErrorMessage(err, false))
		}
		os.Exit(errs.Code(err))
	}
}

func topLevelErrorMessage(err error, _ bool) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:    "lazure",
		Usage:   "Deploy and manage Azure Container Apps",
		Version: versionString(),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "log-level",
				Usage:    "log verbosity: debug|info|warn|error",
				Value:    "info",
				Sources:  cli.EnvVars("LAZURE_LOG_LEVEL"),
				Category: "Global",
			},
			&cli.BoolFlag{
				Name:     "verbose",
				Aliases:  []string{"v"},
				Usage:    "shortcut for --log-level=debug",
				Category: "Global",
			},
			&cli.BoolFlag{
				Name:     "quiet",
				Aliases:  []string{"q"},
				Usage:    "shortcut for --log-level=warn (errors and warnings only)",
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
		Before: func(ctx context.Context, c *cli.Command) (context.Context, error) {
			// Resolve verbosity from -v/-q shortcuts before falling back
			// to --log-level. -v wins over -q if both somehow slip in.
			level := c.String("log-level")
			if c.Bool("verbose") {
				level = "debug"
			} else if c.Bool("quiet") {
				level = "warn"
			}
			if err := logging.Setup(level, c.String("log-format")); err != nil {
				return ctx, err
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			// Deploy pipeline
			{
				Name:   "config",
				Usage:  "print Container App field mapping rules as YAML",
				Action: cmd.Config,
				Description: `Config prints Lazure's Container App ownership rules in YAML.
Use it to see which Azure fields deploy manages, preserves, ignores, normalizes, or rejects.

Examples:
  lazure config                          print field ownership mapping rules`,
			},
			{
				Name:          "deploy",
				Usage:         "deploy to an environment",
				Arguments:     envArg(),
				Flags:         cmd.DeployFlags(),
				Action:        cmd.Deploy,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Deploy renders one environment, verifies ACR image tags, and sends it to Azure Container Apps.
Use --build to build and push the image first. Use --env for one-off plain env vars that apply only to this deployment.

Examples:
  lazure deploy dev                       interactive, with confirm
  lazure deploy dev -y                    non-interactive (CI)
  lazure deploy dev --build               pull base images, build, push, deploy
  lazure deploy dev --env LOG_LEVEL=debug deploy once with a temporary env var
  lazure deploy dev --wait --logs         block until live + tail logs
  lazure deploy dev --force               force a new revision with a timestamp env
  lazure deploy dev --print               preview ARM payload first`,
			},
			{
				Name:          "render",
				Usage:         "print generated ARM YAML to stdout",
				Arguments:     envArg(),
				Action:        cmd.Render,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Render shows the exact Azure Container Apps payload Lazure would send.
Use it to review the final result of templates, vars, defaults, and secrets before changing Azure.

Examples:
  lazure render dev                       to stdout
  lazure render dev > rendered.yml        capture to file
  lazure render dev | yq '.properties'    inspect a section`,
			},
			{
				Name:          "diff",
				Usage:         "diff rendered template vs deployed app",
				Arguments:     envArg(),
				Flags:         cmd.DiffFlags(),
				Action:        cmd.Diff,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Diff compares the app running in Azure with the config rendered from this repo.
It helps catch drift: changes made in Azure, missing deploys, or local config that has not been applied.

Examples:
  lazure diff dev                         colorized unified diff
  lazure diff dev --no-color              plain output (CI / piping)
  lazure diff dev --format=json           machine-readable

Exits 0 if no drift, 1 if drift detected.`,
			},
			{
				Name:          "build",
				Usage:         "build (and optionally push) the docker image for an environment",
				Arguments:     envArg(),
				Flags:         cmd.BuildFlags(),
				Action:        cmd.Build,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Build creates the Docker image for one environment and can push it to ACR.
Lazure reads the image name from vars and adds build details such as commit, branch, and build date.

Examples:
  lazure build dev                        build with auto-injected build-args
  lazure build dev --pull                 always pull base images
  lazure build dev --push                 build then docker push to ACR
  lazure build dev --build-arg KEY=VAL    pass extra --build-arg
  lazure build dev --secret id=tok,env=GH pass docker build secrets

Auto-injected build-args (every build): GIT_COMMIT, APP_VERSION,
GIT_BRANCH, BUILD_DATE. Image tag comes from the env's docker_image
var; ACR registry from acr_server.`,
			},
			{
				Name:   "release",
				Usage:  "cut a calver tag and push",
				Flags:  cmd.ReleaseFlags(),
				Action: cmd.Release,
				Description: `Release creates a dated git tag and pushes it to origin.
In app repositories, that tag normally starts the production GitHub Actions pipeline.

Examples:
  lazure release                          interactive (preview + confirm)
  lazure release -y                       non-interactive
  lazure release --wait                   tail GH Actions until done
  lazure release --force                  include a force redeploy timestamp marker
  lazure release --dry-run                show plan, exit without push`,
			},
			{
				Name:   "self-update",
				Usage:  "update the lazure binary from GitHub releases",
				Flags:  cmd.SelfUpdateFlags(),
				Action: cmd.SelfUpdate,
				Description: `Self-update replaces the current lazure binary with the latest GitHub release.
Use --check when you only want to know whether a newer version exists.

Examples:
  lazure self-update --check              report only (exit 1 if newer available)
  lazure self-update                      download + atomic replace`,
			},

			// Ops / day-two
			{
				Name:          "status",
				Usage:         "show current state of the deployed app",
				Arguments:     envArg(),
				Flags:         cmd.StatusFlags(),
				Action:        cmd.Status,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Status shows what Azure currently knows about the app.
It includes the latest revision, running state, network address, replicas, volumes, registry, and containers.`,
			},
			{
				Name:          "logs",
				Usage:         "stream container logs",
				Arguments:     envArg(),
				Flags:         cmd.LogsFlags(),
				Action:        cmd.Logs,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Logs reads recent container output from Azure.
Use it to check startup errors, health checks, requests, and application messages without opening the Azure portal.

Examples:
  lazure logs dev                         last 20 lines, exit
  lazure logs dev --follow                tail live (Ctrl-C to stop)
  lazure logs dev --tail=100              more history before exit
  lazure logs dev --container=tasks       pick a non-default container
  lazure logs dev --raw                   no JSON parsing / coloring`,
			},
			{
				Name:          "revisions",
				Usage:         "list recent revisions",
				Arguments:     envArg(),
				Flags:         cmd.RevisionsFlags(),
				Action:        cmd.Revisions,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Revisions lists recent versions of the Container App.
Use it to see what is active, what is ready, and which revision you can inspect, restart, or roll back to.`,
			},
			{
				Name:          "ports",
				Usage:         "show ingress URL + target port for an environment",
				Arguments:     envArg(),
				Action:        cmd.Ports,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Ports prints the public app URL and the container port Azure sends traffic to.
Use it when you need the endpoint for a browser, curl, webhook, or health check.`,
			},
			{
				Name:          "scale",
				Usage:         "set replica bounds without editing the manifest",
				Arguments:     envArg(),
				Flags:         cmd.ScaleFlags(),
				Action:        cmd.Scale,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Scale changes how many app replicas Azure may run.
Use it for temporary operations, such as scaling to zero to save cost or raising capacity during traffic spikes.

Examples:
  lazure scale dev --replicas 3            pin to exactly 3 replicas
  lazure scale dev --min 1 --max 10        bound between 1 and 10
  lazure scale dev --max 20                raise ceiling, keep current min
  lazure scale dev --replicas 0 -y         scale to zero (cost-saving)`,
			},
			{
				Name:          "events",
				Usage:         "show recent ARM activity log entries for the app",
				Arguments:     envArg(),
				Flags:         cmd.EventsFlags(),
				Action:        cmd.Events,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Events shows Azure activity log entries for this app.
Use --expand when Azure says a deployment failed and you need the detailed error message from ARM.

Examples:
  lazure events dev                        last 24h, table output
  lazure events dev --since=1h             last hour only
  lazure events dev --since=168h           last 7 days
  lazure events dev --expand               show Azure error details
  lazure events dev --format=json | jq     pipe-friendly`,
			},
			{
				Name:          "validate",
				Usage:         "static pre-flight: render + struct validate + secret refs (no Azure calls)",
				Arguments:     envArg(),
				Action:        cmd.Validate,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Validate checks the local deploy files without changing Azure.
It renders templates, validates the manifest shape, decrypts SOPS secrets, and checks that secret references match.`,
			},
			{
				Name:          "rollback",
				Usage:         "shift traffic to a previous revision",
				Arguments:     envArg(),
				Flags:         cmd.RollbackFlags(),
				Action:        cmd.Rollback,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Rollback moves traffic back to an older revision.
Use it when the latest deploy is bad and a previous revision is still available in Azure.

Examples:
  lazure rollback dev                     interactive picker
  lazure rollback dev --to <rev> -y       non-interactive (CI)
  lazure rollback dev --to <rev> --wait   wait for new replicas`,
			},
			{
				Name:          "restart",
				Usage:         "restart a revision",
				Arguments:     envArg(),
				Flags:         cmd.RestartFlags(),
				Action:        cmd.Restart,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Restart asks Azure to restart one revision's replicas.
Use it for stuck processes, refreshed connections, or retrying an app that should recover without a new deploy.

Examples:
  lazure restart dev                      restart current revision
  lazure restart dev --revision <rev>     restart a specific one
  lazure restart dev --wait               block until replicas Ready
  lazure restart dev --wait --no-logs     wait but don't tail logs`,
			},
			{
				Name:          "exec",
				Usage:         "exec into a container (shells out to az)",
				Arguments:     cmd.ExecArgs(),
				Flags:         cmd.ExecFlags(),
				Action:        cmd.Exec,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Exec opens a command inside a running container through Azure CLI.
Use it for short diagnostics such as listing files, checking environment variables, or opening a shell.

Examples:
  lazure exec dev                         interactive sh in default container
  lazure exec dev --container tasks       pick a non-default container
  lazure exec dev -- ls -la /srv          one-shot command (note the --)
  lazure exec dev -- env | grep DB        chain shell ops via az`,
			},
			{
				Name:   "doctor",
				Usage:  "preflight diagnostic checks",
				Flags:  cmd.DoctorFlags(),
				Action: cmd.Doctor,
				Description: `Doctor checks whether your machine and project are ready for Lazure.
It reports common setup problems: missing tools, Azure auth, GitHub auth, SOPS secrets, vars files, and Azure access.`,
			},

			// Onboarding
			{
				Name:   "init",
				Usage:  "scaffold a new lazure project in the current directory",
				Flags:  cmd.InitFlags(),
				Action: cmd.Init,
				Description: `Init creates the first deploy directory for a new app.
It writes a starter manifest, env vars, secrets files, and schema wiring so the project can be edited safely.

Examples:
  lazure init                             interactive (prompts for name etc.)
  lazure init --quiet --name api --resource-group app-rg
  lazure init --force                     overwrite an existing deploy.yml`,
			},
			{
				Name:  "env",
				Usage: "inspect environments",
				Description: `Env commands help compare and list the environments configured in deploy/envs.
Use them before deploys to spot missing vars or secrets across dev, uat, and prd.`,
				Commands: []*cli.Command{
					{
						Name:   "list",
						Usage:  "print available envs (one per line)",
						Action: cmd.EnvList,
						Description: `Env list prints the environment names Lazure finds under deploy/envs.
It is useful for scripts, shell completion, and checking which environments exist.`,
					},
					{
						Name:   "diff",
						Usage:  "matrix of vars + secret refs across all envs (catches asymmetries)",
						Action: cmd.EnvDiff,
						Description: `Env diff compares vars and secret references across all environments.
It helps catch mistakes such as a secret used in dev but missing in prd.

Examples:
  lazure env diff                          show full vars + secrets matrix

Marks:
  ✓  defined / referenced and present
  ✗  referenced in manifest but missing from SOPS file (deploy will fail)
  ○  defined in SOPS but not referenced anywhere (orphan)
  —  not applicable in this env`,
					},
				},
			},
			cmd.EnvGuessCommand(),
			{
				Name:      "schema",
				Usage:     "write the embedded JSON Schema for deploy.yml to disk or stdout",
				Arguments: cmd.SchemaArgs(),
				Action:    cmd.Schema,
				Description: `Schema writes the JSON Schema that editors use for deploy.yml autocomplete and validation.
Run it after updating Lazure so your editor knows the latest manifest fields.

Examples:
  lazure schema                           write to <dir>/deploy.schema.json
  lazure schema -                         write to stdout (pipe to jq etc.)
  lazure schema /tmp/lazure.json          write to a specific path`,
			},

			// Config surface
			cmd.SecretsCommand(),
			cmd.VarsCommand(),

			// Documentation
			{
				Name:  "prime",
				Usage: "print agent operating guidance and the full CLI reference",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "emit a structured JSON document instead of markdown"},
				},
				Action: cmd.Prime,
				Description: `Prime prints Lazure operating guidance and the full command reference for coding agents.
Use it at the start of an agent session so the agent understands Lazure-managed workflows, command names, flags, prerequisites, and dependencies.

Examples:
  lazure prime                            print agent guide + full reference (markdown)
  lazure prime --json                     structured JSON for tool consumption
  lazure prime >> AGENTS.md               append the guide to an agent instructions file
  lazure prime --json | jq '.categories[].commands[].path'  list every command`,
			},
		},
		EnableShellCompletion: true,
	}
}

func envArg() []cli.Argument {
	return []cli.Argument{
		&cli.StringArg{Name: "env", UsageText: "target environment (dev|uat|prd|...)"},
	}
}
