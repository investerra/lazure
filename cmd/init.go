package cmd

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/schema"
)

// InitFlags are the flags for `lazure init`.
func InitFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "name", Usage: "app name"},
		&cli.StringFlag{Name: "location", Value: "switzerlandnorth", Usage: "Azure region"},
		&cli.StringFlag{Name: "resource-group", Usage: "Azure resource group name"},
		&cli.StringFlag{Name: "envs", Value: "dev,uat,prd", Usage: "comma-separated list of environments to scaffold"},
		&cli.BoolFlag{Name: "quiet", Usage: "no prompts — all values must be provided via flags"},
		&cli.BoolFlag{Name: "force", Usage: "overwrite existing deploy.yml"},
	}
}

// Init implements `lazure init`. Scaffolds a new project layout in
// --dir (default ./deploy) without making any Azure API calls. Safe to
// re-run with --force to regenerate templates after editing.
//
// The scaffold intentionally leaves identity/image/managed-env IDs as
// TODO placeholders: those values are environment-specific and are the
// things the user will fill in from the infra side. The manifest
// skeleton has commented-out sections for ingress/scale/volumes so
// users can uncomment features as they need them rather than starting
// from a maximal config.
func Init(ctx context.Context, c *cli.Command) error {
	dir := c.String("dir")
	quiet := c.Bool("quiet")
	force := c.Bool("force")
	slog.Debug("init: start", "dir", dir, "quiet", quiet, "force", force)

	manifestPath := filepath.Join(dir, "deploy.yml")
	if _, err := os.Stat(manifestPath); err == nil && !force {
		return errs.Usage(errs.Errorf("init: %s already exists — run with --force to overwrite", manifestPath))
	}

	cfg, err := collectInitConfig(c, quiet)
	if err != nil {
		return errs.Usage(err)
	}
	if err := validateInitConfig(cfg); err != nil {
		return errs.Usage(err)
	}

	inferences := inferAll(ctx, cfg)
	if err := scaffoldProject(dir, cfg, inferences); err != nil {
		return errs.System(errs.Wrap(err, "init: write project"))
	}
	if err := updateGitignore(".gitignore", []string{"envs/*.plain.yml", ".lazure/"}); err != nil {
		return errs.System(errs.Wrap(err, "init: update .gitignore"))
	}

	printInferenceSummary(inferences)
	printNextSteps(cfg, dir)
	return nil
}

// ---------- config ----------

type initConfig struct {
	Name          string
	Location      string
	ResourceGroup string
	Envs          []string
}

// collectInitConfig merges flag values with interactive prompts (if
// !quiet) into a fully-populated initConfig. --quiet skips prompts
// entirely and relies on flag values; missing required flags produce a
// usage error that lists ALL missing ones at once so users don't have
// to iterate one flag at a time.
func collectInitConfig(c *cli.Command, quiet bool) (initConfig, error) {
	cfg := initConfig{
		Name:          c.String("name"),
		Location:      c.String("location"),
		ResourceGroup: c.String("resource-group"),
	}
	envs, err := parseEnvsCSV(c.String("envs"))
	if err != nil {
		return initConfig{}, errs.Wrap(err, "init: --envs")
	}
	cfg.Envs = envs

	if quiet {
		var missing []string
		if cfg.Name == "" {
			missing = append(missing, "--name")
		}
		if cfg.ResourceGroup == "" {
			missing = append(missing, "--resource-group")
		}
		if len(missing) > 0 {
			return initConfig{}, errs.Errorf("init: --quiet requires %s", strings.Join(missing, ", "))
		}
		return cfg, nil
	}

	reader := bufio.NewReader(os.Stdin)
	cfg.Name = promptWithDefault(reader, "app name", cfg.Name)
	cfg.Location = promptWithDefault(reader, "location", cfg.Location)
	cfg.ResourceGroup = promptWithDefault(reader, "resource group", cfg.ResourceGroup)

	envStr := promptWithDefault(reader, "envs (comma-separated)", strings.Join(cfg.Envs, ","))
	newEnvs, err := parseEnvsCSV(envStr)
	if err != nil {
		return initConfig{}, errs.Wrap(err, "init: envs")
	}
	cfg.Envs = newEnvs
	return cfg, nil
}

// promptWithDefault shows `default` bracketed; empty input returns it
// unchanged. Non-empty input replaces it. Trims trailing whitespace so
// copy-pasted values with trailing newlines still work.
func promptWithDefault(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// parseEnvsCSV splits a comma-separated env list, trims each entry,
// drops empties, and rejects duplicates. Duplicates specifically fail
// rather than being silently deduped so users notice typos like
// "dev,dev" (meant "dev,uat").
func parseEnvsCSV(s string) ([]string, error) {
	var envs []string
	seen := map[string]bool{}
	for part := range strings.SplitSeq(s, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if seen[p] {
			return nil, errs.Errorf("duplicate env %q", p)
		}
		seen[p] = true
		envs = append(envs, p)
	}
	if len(envs) == 0 {
		return nil, errs.New("envs list is empty")
	}
	return envs, nil
}

func validateInitConfig(cfg initConfig) error {
	if cfg.Name == "" {
		return errs.New("init: app name is required")
	}
	if cfg.Location == "" {
		return errs.New("init: location is required")
	}
	if cfg.ResourceGroup == "" {
		return errs.New("init: resource-group is required")
	}
	if len(cfg.Envs) == 0 {
		return errs.New("init: at least one env is required")
	}
	return nil
}

// ---------- scaffold ----------

func scaffoldProject(dir string, cfg initConfig, inf projectInferences) error {
	envsDir := filepath.Join(dir, "envs")
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(dir, "deploy.yml"), []byte(renderDeployYml(cfg)), 0o644); err != nil {
		return err
	}
	// Ship the JSON Schema next to deploy.yml so the modeline's
	// `$schema=./deploy.schema.json` resolves without network access.
	// Users regenerate after a lazure upgrade via `lazure schema`.
	if err := os.WriteFile(filepath.Join(dir, "deploy.schema.json"), schemaWithNewline(), 0o644); err != nil {
		return err
	}
	for _, env := range cfg.Envs {
		varsPath := filepath.Join(envsDir, env+".vars.yml")
		body := renderVarsYml(env, cfg, inf.byEnv[env], inf.gitOrg)
		if err := os.WriteFile(varsPath, []byte(body), 0o644); err != nil {
			return err
		}
	}
	// Plaintext secrets sidecar for the first env only — it's a
	// throwaway that the user replaces with `lazure secrets edit` on
	// first run. Creating it for every env would force them to run
	// edit on every env just to clear the TODO.
	firstEnv := cfg.Envs[0]
	plainPath := filepath.Join(envsDir, firstEnv+".secrets.plain.yml")
	return os.WriteFile(plainPath, []byte(renderSecretsPlain(firstEnv)), 0o600)
}

// schemaWithNewline returns the embedded schema bytes with a trailing
// newline. Centralized so both `init` and `lazure schema` produce
// byte-identical files (matters for CI drift checks).
func schemaWithNewline() []byte { return withTrailingNewline(schema.JSON) }

// renderDeployYml returns the manifest skeleton. Values from cfg are
// inlined; everything else is commented examples so users uncomment
// features as they adopt them instead of starting from a maximal
// config they then have to whittle down.
func renderDeployYml(cfg initConfig) string {
	return fmt.Sprintf(`# yaml-language-server: $schema=./deploy.schema.json
app:
  name: %s
  location: %s
  resource_group: "{{ .Vars.resource_group }}"
  managed_environment_id: "{{ .Vars.managed_environment_id }}"
  identity: "{{ .Vars.user_assigned_identity_id }}"

# ingress:
#   external: true
#   target_port: 8000
#   transport: auto

# scale:
#   min: 1
#   max: 3
#   rules:
#     - name: http
#       http: { concurrent_requests: 10 }

# volumes:
#   - name: cache
#     type: empty_dir

env:
  APP_ENV: "{{ .Vars.app_env }}"
  LOG_LEVEL: info
  # DATABASE_URL: { secret: database-url }

containers:
  - name: app
    image: "{{ .Vars.docker_image }}"
    resources: { cpu: 0.5, memory: 1Gi }
    # probes:
    #   liveness:  { http: { path: /health, port: 8000 }, initial_delay: 10, period: 30 }
    #   readiness: { http: { path: /health, port: 8000 }, initial_delay: 5,  period: 10 }
`, cfg.Name, cfg.Location)
}

// renderVarsYml emits a per-env vars.yml. Inferred values (when
// non-empty in `envInf`) replace the TODO placeholders; missing ones
// keep the placeholder + TODO comment so the user notices. The
// resource_group line follows the <env>-<baseRG> convention.
func renderVarsYml(env string, cfg initConfig, envInf envInference, gitOrg string) string {
	rg := fmt.Sprintf("%s-%s", env, cfg.ResourceGroup)

	managedEnv := envInf.ManagedEnv
	managedEnvTodo := ""
	if managedEnv == "" {
		managedEnv = "/subscriptions/SUB/resourceGroups/RG/providers/Microsoft.App/managedEnvironments/ME"
		managedEnvTodo = "  # TODO: full resource ID from your infra"
	}

	identity := envInf.Identity
	identityTodo := ""
	if identity == "" {
		identity = "/subscriptions/SUB/resourceGroups/RG/providers/Microsoft.ManagedIdentity/userAssignedIdentities/MI"
		identityTodo = "  # TODO: full resource ID from your infra"
	}

	acr := envInf.ACRServer
	acrTodo := ""
	if acr == "" {
		acr = "yourregistry.azurecr.io"
		acrTodo = "  # TODO: set your ACR login server"
	}

	image := composeDockerImage(envInf.ACRServer, gitOrg, cfg.Name)
	imageTodo := ""
	if image == "" {
		image = fmt.Sprintf("yourregistry.azurecr.io/yourorg/%s:{{ .Vars.git_commit }}", cfg.Name)
		imageTodo = "  # TODO: image path + tag (git_commit stdvar is available)"
	}

	return fmt.Sprintf(`# deploy/envs/%[1]s.vars.yml
# Environment-specific values rendered into deploy.yml as .Vars.*
# After any edits, run: lazure render %[1]s

resource_group: %[2]s  # TODO: adjust if your naming differs

managed_environment_id: %[3]s%[4]s
user_assigned_identity_id: %[5]s%[6]s

acr_server: %[7]s%[8]s
docker_image: %[9]q%[10]s

app_env: %[1]s
`,
		env,
		rg,
		managedEnv, managedEnvTodo,
		identity, identityTodo,
		acr, acrTodo,
		image, imageTodo,
	)
}

// composeDockerImage returns "" (→ caller keeps a TODO placeholder)
// when we can't build a useful value. When ACR is known, the image
// path is "<acr>/<org>/<app>:{{ .Vars.git_commit }}" if the git org
// is also known, else "<acr>/<app>:{{ .Vars.git_commit }}". The
// git_commit template reference is a stdvar injected by lazure's
// two-pass render, so it resolves at deploy time.
func composeDockerImage(acr, org, app string) string {
	if acr == "" || app == "" {
		return ""
	}
	if org == "" {
		return fmt.Sprintf("%s/%s:{{ .Vars.git_commit }}", acr, app)
	}
	return fmt.Sprintf("%s/%s/%s:{{ .Vars.git_commit }}", acr, org, app)
}

func renderSecretsPlain(env string) string {
	return fmt.Sprintf(`# deploy/envs/%[1]s.secrets.plain.yml
#
# This is a throwaway plaintext file — fill in secrets below, then run:
#   lazure secrets edit %[1]s
# which will encrypt them to %[1]s.secrets.yml and remove this file.
#
# Secret names should be kebab-case; values are strings.
# Reference them in deploy.yml with { secret: name-of-secret }.
#
# Example:
# database-url: "postgresql://user:pass@host:5432/db"
# redis-url: "redis://host:6379"
`, env)
}

// ---------- .gitignore ----------

// updateGitignore creates the file if absent or appends any missing
// patterns. Already-present patterns are left alone — we avoid
// rewriting the file unnecessarily. Comparison is literal-line match
// (anchored, whitespace-trimmed); semantic gitignore subsumption is
// not attempted.
func updateGitignore(path string, patterns []string) error {
	existing := ""
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	} else if !os.IsNotExist(err) {
		return err
	}
	missing := gitignorePatternsToAppend(existing, patterns)
	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(existing)
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		b.WriteString("\n")
	}
	if existing != "" {
		b.WriteString("\n# lazure\n")
	} else {
		b.WriteString("# lazure\n")
	}
	for _, p := range missing {
		b.WriteString(p)
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func gitignorePatternsToAppend(existing string, want []string) []string {
	have := map[string]bool{}
	for line := range strings.SplitSeq(existing, "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, p := range want {
		if !have[p] {
			missing = append(missing, p)
		}
	}
	return missing
}

// ---------- next steps ----------

func printNextSteps(cfg initConfig, dir string) {
	envs := append([]string{}, cfg.Envs...)
	sort.Strings(envs)
	first := envs[0]

	fmt.Printf("\nlazure project scaffolded in %s/\n\n", dir)
	fmt.Printf("next steps:\n")
	fmt.Printf("  1. edit deploy/envs/*.vars.yml — fill in the TODO IDs\n")
	fmt.Printf("  2. lazure secrets edit %s    # encrypt your first env's secrets\n", first)
	fmt.Printf("  3. lazure render %s            # preview the ARM output\n", first)
	fmt.Printf("  4. lazure doctor                 # run preflight checks\n")
	fmt.Printf("  5. lazure deploy %s            # ship it\n\n", first)
}

// ---------- inference ----------
//
// All Azure lookups are best-effort: any failure (az missing, not
// logged in, nothing found, ambiguous result, network hiccup) produces
// an empty string and the rendered vars.yml keeps the TODO placeholder.
// Init never fails because of inference — the whole point is to save
// keystrokes when the happy path works and degrade invisibly otherwise.

type envInference struct {
	ManagedEnv string
	Identity   string
	ACRServer  string
}

type projectInferences struct {
	gitOrg string
	byEnv  map[string]envInference
}

// inferAll runs one git-remote lookup and one az round per env. az
// invocations are sequential — 3 envs × 3 resources × ~1s each is
// still well under 10s and parallelism would complicate error
// handling for near-zero benefit at this scale.
func inferAll(ctx context.Context, cfg initConfig) projectInferences {
	out := projectInferences{byEnv: map[string]envInference{}}
	out.gitOrg = inferGitOrg()

	if _, err := exec.LookPath("az"); err != nil {
		slog.Debug("init: az not on PATH — skipping Azure inference")
		return out
	}
	for _, env := range cfg.Envs {
		rg := fmt.Sprintf("%s-%s", env, cfg.ResourceGroup)
		inf := envInference{
			ManagedEnv: singleAzID(ctx, "containerapp", "env", "list", "-g", rg, "--query", "[].id", "-o", "tsv"),
			Identity:   singleAzID(ctx, "identity", "list", "-g", rg, "--query", "[].id", "-o", "tsv"),
			ACRServer:  singleAzID(ctx, "acr", "list", "-g", rg, "--query", "[].loginServer", "-o", "tsv"),
		}
		out.byEnv[env] = inf
		slog.Debug("init: env inference",
			"env", env, "rg", rg,
			"managed_env", inf.ManagedEnv != "",
			"identity", inf.Identity != "",
			"acr", inf.ACRServer != "")
	}
	return out
}

// singleAzID runs `az <args>` and returns stdout trimmed ONLY when the
// output is a single non-empty line. Zero or multiple lines → empty
// string (the scaffold keeps the TODO placeholder rather than guess
// which of N resources to use).
func singleAzID(ctx context.Context, args ...string) string {
	out, err := exec.CommandContext(ctx, "az", args...).Output()
	if err != nil {
		return ""
	}
	lines := strings.FieldsFunc(string(out), func(r rune) bool { return r == '\n' || r == '\r' })
	var nonEmpty []string
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	if len(nonEmpty) != 1 {
		return ""
	}
	return nonEmpty[0]
}

// inferGitOrg extracts the org segment from `git remote get-url origin`.
// Returns "" on any parse failure; the caller falls back to omitting
// the org segment in the composed image path.
func inferGitOrg() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	_, org, _ := parseGitRemote(strings.TrimSpace(string(out)))
	return org
}

// parseGitRemote splits a git remote URL into its host, org, and repo.
// Handles both SSH (git@host:org/repo.git) and HTTPS/URL
// (https://host/org/repo.git) forms. Returns all three empty on any
// unrecognized shape.
func parseGitRemote(url string) (host, org, repo string) {
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	url = strings.TrimSuffix(url, "/")
	// SSH form: git@host:org/repo
	if strings.HasPrefix(url, "git@") {
		rest := strings.TrimPrefix(url, "git@")
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return "", "", ""
		}
		host = rest[:colon]
		parts := strings.SplitN(rest[colon+1:], "/", 2)
		if len(parts) != 2 {
			return "", "", ""
		}
		return host, parts[0], parts[1]
	}
	// URL form: scheme://host/org/repo (or without scheme)
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}
	u, err := neturl.Parse(url)
	if err != nil {
		return "", "", ""
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ""
	}
	return u.Host, parts[0], parts[1]
}

// printInferenceSummary reports which fields were auto-filled vs kept
// as TODO, per env. Runs after scaffolding so the user can verify
// before editing. When no inference was possible anywhere (no az, no
// git remote), prints nothing — the TODOs in the files are
// self-explanatory.
func printInferenceSummary(inf projectInferences) {
	any := inf.gitOrg != ""
	for _, e := range inf.byEnv {
		if e.ManagedEnv != "" || e.Identity != "" || e.ACRServer != "" {
			any = true
			break
		}
	}
	if !any {
		return
	}

	fmt.Printf("\ninferred from your environment:\n")
	if inf.gitOrg != "" {
		fmt.Printf("  ✓ git org       %s (from `git remote get-url origin`)\n", inf.gitOrg)
	}

	envs := make([]string, 0, len(inf.byEnv))
	for e := range inf.byEnv {
		envs = append(envs, e)
	}
	sort.Strings(envs)
	for _, env := range envs {
		e := inf.byEnv[env]
		fmt.Printf("  %s %-6s  managed_env:%s  identity:%s  acr:%s\n",
			overallMark(e), env,
			tick(e.ManagedEnv), tick(e.Identity), tick(e.ACRServer))
	}
}

func tick(s string) string {
	if s != "" {
		return " ✓"
	}
	return " —"
}

func overallMark(e envInference) string {
	if e.ManagedEnv != "" && e.Identity != "" && e.ACRServer != "" {
		return "✓"
	}
	if e.ManagedEnv == "" && e.Identity == "" && e.ACRServer == "" {
		return "—"
	}
	return "~"
}
