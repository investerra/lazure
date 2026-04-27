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
			// --log-level=debug prints the full %+v stack trace chain
			// that pkg/errors records; otherwise a one-line message.
			if slog.Default().Enabled(ctx, slog.LevelDebug) {
				slog.Error(fmt.Sprintf("%+v", err))
			} else {
				slog.Error(err.Error())
			}
		}
		os.Exit(errs.Code(err))
	}
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
				Name:          "deploy",
				Usage:         "deploy to an environment",
				Arguments:     envArg(),
				Flags:         cmd.DeployFlags(),
				Action:        cmd.Deploy,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Examples:
  lazure deploy dev                       interactive, with confirm
  lazure deploy dev -y                    non-interactive (CI)
  lazure deploy dev --wait --logs         block until live + tail logs
  lazure deploy dev --print               preview ARM payload first`,
			},
			{
				Name:          "render",
				Usage:         "print generated ARM YAML to stdout",
				Arguments:     envArg(),
				Action:        cmd.Render,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Examples:
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
				Description: `Examples:
  lazure diff dev                         colorized unified diff
  lazure diff dev --no-color              plain output (CI / piping)
  lazure diff dev --format=json           machine-readable

Exits 0 if no drift, 1 if drift detected.`,
			},
			{
				Name:          "rollout",
				Usage:         "build + push + deploy in one step",
				Arguments:     envArg(),
				Flags:         cmd.RolloutFlags(),
				Action:        cmd.Rollout,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Examples:
  lazure rollout dev                      build + push + deploy
  lazure rollout dev -y                   non-interactive deploy
  lazure rollout dev --no-build           skip build, deploy already-pushed image
  lazure rollout dev --pull               always pull base images during build`,
			},
			{
				Name:          "build",
				Usage:         "build (and optionally push) the docker image for an environment",
				Arguments:     envArg(),
				Flags:         cmd.BuildFlags(),
				Action:        cmd.Build,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Examples:
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
				Description: `Examples:
  lazure release                          interactive (preview + confirm)
  lazure release -y                       non-interactive
  lazure release --wait                   tail GH Actions until done
  lazure release --dry-run                show plan, exit without push`,
			},
			{
				Name:   "self-update",
				Usage:  "update the lazure binary from GitHub releases",
				Flags:  cmd.SelfUpdateFlags(),
				Action: cmd.SelfUpdate,
				Description: `Examples:
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
			},
			{
				Name:          "logs",
				Usage:         "stream container logs",
				Arguments:     envArg(),
				Flags:         cmd.LogsFlags(),
				Action:        cmd.Logs,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Examples:
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
			},
			{
				Name:          "ports",
				Usage:         "show ingress URL + target port for an environment",
				Arguments:     envArg(),
				Action:        cmd.Ports,
				ShellComplete: cmd.CompleteEnvs,
			},
			{
				Name:          "scale",
				Usage:         "set replica bounds without editing the manifest",
				Arguments:     envArg(),
				Flags:         cmd.ScaleFlags(),
				Action:        cmd.Scale,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Examples:
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
				Description: `Examples:
  lazure events dev                        last 24h, table output
  lazure events dev --since=1h             last hour only
  lazure events dev --since=168h           last 7 days
  lazure events dev --format=json | jq     pipe-friendly`,
			},
			{
				Name:          "validate",
				Usage:         "static pre-flight: render + struct validate + secret refs (no Azure calls)",
				Arguments:     envArg(),
				Action:        cmd.Validate,
				ShellComplete: cmd.CompleteEnvs,
			},
			{
				Name:          "rollback",
				Usage:         "shift traffic to a previous revision",
				Arguments:     envArg(),
				Flags:         cmd.RollbackFlags(),
				Action:        cmd.Rollback,
				ShellComplete: cmd.CompleteEnvs,
				Description: `Examples:
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
				Description: `Examples:
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
				Description: `Examples:
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
			},

			// Onboarding
			{
				Name:   "init",
				Usage:  "scaffold a new lazure project in the current directory",
				Flags:  cmd.InitFlags(),
				Action: cmd.Init,
				Description: `Examples:
  lazure init                             interactive (prompts for name etc.)
  lazure init --quiet --name api --resource-group dbx
  lazure init --force                     overwrite an existing deploy.yml`,
			},
			{
				Name:  "env",
				Usage: "inspect environments",
				Commands: []*cli.Command{
					{
						Name:   "list",
						Usage:  "print available envs (one per line)",
						Action: cmd.EnvList,
					},
					{
						Name:   "diff",
						Usage:  "matrix of vars + secret refs across all envs (catches asymmetries)",
						Action: cmd.EnvDiff,
						Description: `Examples:
  lazure env diff                          show full vars + secrets matrix

Marks:
  ✓  defined / referenced and present
  ✗  referenced in manifest but missing from SOPS file (deploy will fail)
  ○  defined in SOPS but not referenced anywhere (orphan)
  —  not applicable in this env`,
					},
				},
			},
			{
				Name:      "schema",
				Usage:     "write the embedded JSON Schema for deploy.yml to disk or stdout",
				Arguments: cmd.SchemaArgs(),
				Action:    cmd.Schema,
				Description: `Examples:
  lazure schema                           write to <dir>/deploy.schema.json
  lazure schema -                         write to stdout (pipe to jq etc.)
  lazure schema /tmp/lazure.json          write to a specific path`,
			},

			// Config surface
			cmd.SecretsCommand(),
			cmd.VarsCommand(),
		},
		EnableShellCompletion: true,
	}
}

func envArg() []cli.Argument {
	return []cli.Argument{
		&cli.StringArg{Name: "env", UsageText: "target environment (dev|uat|prd|...)"},
	}
}
