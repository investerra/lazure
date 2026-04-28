package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/urfave/cli/v3"
)

// Llm implements `lazure llm`. Walks the command tree from the root,
// builds a structured documentation tree, and emits it either as
// markdown (default) or JSON (--json).
func Llm(ctx context.Context, c *cli.Command) error {
	root := c.Root()
	doc := buildDoc(root)
	if c.Bool("json") {
		return emitJSON(doc)
	}
	return emitMarkdown(doc)
}

// ---------------------------- doc model ----------------------------

type Doc struct {
	Name        string        `json:"name"`
	Usage       string        `json:"usage,omitempty"`
	Invocation  string        `json:"invocation,omitempty"`
	Notes       []string      `json:"notes,omitempty"`
	GlobalFlags []FlagDoc     `json:"global_flags,omitempty"`
	Categories  []CategoryDoc `json:"categories"`
}

type CategoryDoc struct {
	Name     string       `json:"name"`
	Commands []CommandDoc `json:"commands"`
}

type CommandDoc struct {
	Path          string       `json:"path"`
	Name          string       `json:"name"`
	Usage         string       `json:"usage,omitempty"`
	UseCase       string       `json:"use_case,omitempty"`
	Prerequisites []string     `json:"prerequisites,omitempty"`
	Dependencies  []string     `json:"dependencies,omitempty"`
	Arguments     []ArgDoc     `json:"arguments,omitempty"`
	Flags         []FlagDoc    `json:"flags,omitempty"`
	Examples      string       `json:"examples,omitempty"`
	Subcommands   []CommandDoc `json:"subcommands,omitempty"`
}

type ArgDoc struct {
	Name     string `json:"name"`
	Usage    string `json:"usage,omitempty"`
	Required bool   `json:"required"`
	Variadic bool   `json:"variadic,omitempty"`
}

type FlagDoc struct {
	Names    []string `json:"names"`
	Usage    string   `json:"usage,omitempty"`
	Type     string   `json:"type,omitempty"`
	Default  string   `json:"default,omitempty"`
	EnvVars  []string `json:"env_vars,omitempty"`
	Required bool     `json:"required,omitempty"`
}

// ---------------------------- metadata -----------------------------

// commandCategory groups top-level commands. Hardcoded by command name
// so main.go stays focused on registration.
var commandCategory = map[string]string{
	"deploy":      "Deploy pipeline",
	"render":      "Deploy pipeline",
	"diff":        "Deploy pipeline",
	"rollout":     "Deploy pipeline",
	"build":       "Deploy pipeline",
	"release":     "Deploy pipeline",
	"self-update": "Deploy pipeline",

	"status":    "Ops / day-two",
	"logs":      "Ops / day-two",
	"revisions": "Ops / day-two",
	"ports":     "Ops / day-two",
	"scale":     "Ops / day-two",
	"events":    "Ops / day-two",
	"validate":  "Ops / day-two",
	"rollback":  "Ops / day-two",
	"restart":   "Ops / day-two",
	"exec":      "Ops / day-two",
	"doctor":    "Ops / day-two",

	"init":   "Onboarding",
	"env":    "Onboarding",
	"schema": "Onboarding",

	"secrets": "Config surface",
	"vars":    "Config surface",

	"llm": "Documentation",
}

var categoryOrder = []string{
	"Deploy pipeline",
	"Ops / day-two",
	"Config surface",
	"Onboarding",
	"Documentation",
}

// commandMeta carries hand-curated context for a command path: what the
// command is for, what must be true before running it, and what
// external systems / binaries it touches.
type commandMeta struct {
	useCase       string
	prerequisites []string
	dependencies  []string
}

// Reusable prerequisite snippets so we don't drift between commands.
var (
	prereqAzureAuth = "Authenticated Azure session: `az login` (interactive) or `AZURE_CLIENT_ID` + `AZURE_CLIENT_SECRET` + `AZURE_TENANT_ID` env vars (CI / service principal)."
	prereqManifest  = "Valid `deploy.yml` and `deploy/envs/<env>.vars.yml` (run `lazure validate <env>` first if unsure)."
	prereqAppLive   = "Target Container App must already be deployed (run `lazure deploy <env>` once before using day-two commands)."
	prereqSops      = "`.sops.yaml` config in the project root and an age/PGP key available on the workstation (env var `SOPS_AGE_KEY_FILE` or `~/.config/sops/age/keys.txt`)."
	prereqEditor    = "`$EDITOR` (or `$VISUAL` for `secrets edit`) set to a launcher that exits non-zero on failure."

	depAzureARM = "Azure ARM REST API (network access to `management.azure.com`)."
	depSops     = "`sops` binary on PATH."
	depDocker   = "`docker` binary on PATH; running docker daemon."
	depAzCLI    = "`az` CLI binary on PATH (lazure shells out for this command)."
	depGit      = "`git` binary on PATH."
)

var commandMetadata = map[string]commandMeta{
	// ---------- Deploy pipeline ----------
	"lazure deploy": {
		useCase: "you have a built+pushed image and want to roll it out to a target environment; use `--force` to create a fresh revision even when the template would otherwise be unchanged.",
		prerequisites: []string{
			prereqAzureAuth,
			prereqManifest,
			"Image referenced by the env's `docker_image` var must already be pushed to the registry (use `lazure rollout` or `lazure build --push` if not).",
			"Every `{secret: X}` reference in the manifest must already exist in Azure Key Vault (run `lazure secrets sync <env>` first).",
		},
		dependencies: []string{depAzureARM},
	},
	"lazure render": {
		useCase:       "inspect the exact ARM template that would be sent to Azure without applying it.",
		prerequisites: []string{prereqManifest},
	},
	"lazure diff": {
		useCase:       "you suspect drift between the manifest and the live app, or want a CI gate that fails on uncommitted infra changes.",
		prerequisites: []string{prereqAzureAuth, prereqManifest, prereqAppLive},
		dependencies:  []string{depAzureARM},
	},
	"lazure rollout": {
		useCase: "end-to-end shortcut: build, push, then deploy in one command (typical local-dev push to dev/uat).",
		prerequisites: []string{
			prereqAzureAuth,
			prereqManifest,
			"Logged-in to the ACR registry (`az acr login -n <registry>`).",
			"Every `{secret: X}` reference in the manifest must already exist in Azure Key Vault.",
		},
		dependencies: []string{depDocker, depAzureARM},
	},
	"lazure build": {
		useCase: "build (and optionally push) the docker image only — useful when you want to control deploy separately from build.",
		prerequisites: []string{
			"Running docker daemon and a `Dockerfile` reachable from the build context.",
			"With `--push`: logged-in to the ACR registry (`az acr login -n <registry>`).",
		},
		dependencies: []string{depDocker},
	},
	"lazure release": {
		useCase: "cut a calver-tagged production release; the tag triggers the production GH Actions pipeline. `--force` records a force redeploy timestamp marker in the tag body for downstream deploy workflows.",
		prerequisites: []string{
			"Clean git working tree on the release branch.",
			"Push access to the `origin` remote.",
			"With `--wait`: `gh` CLI logged in to the repo.",
		},
		dependencies: []string{depGit, "Optional `gh` CLI for `--wait`."},
	},
	"lazure self-update": {
		useCase:       "upgrade the lazure binary itself to the newest GitHub release.",
		prerequisites: []string{"Write access to the directory holding the running `lazure` binary."},
		dependencies:  []string{"GitHub Releases API (network access required)."},
	},

	// ---------- Ops / day-two ----------
	"lazure status": {
		useCase:       "quick health check — what revision is live, ingress URL, replica count.",
		prerequisites: []string{prereqAzureAuth, prereqAppLive},
		dependencies:  []string{depAzureARM},
	},
	"lazure logs": {
		useCase:       "view or tail container stdout/stderr for the running revision.",
		prerequisites: []string{prereqAzureAuth, prereqAppLive},
		dependencies:  []string{depAzureARM},
	},
	"lazure revisions": {
		useCase:       "list past revisions and their traffic weights — useful before a rollback.",
		prerequisites: []string{prereqAzureAuth, prereqAppLive},
		dependencies:  []string{depAzureARM},
	},
	"lazure ports": {
		useCase:       "look up the public ingress URL and target port for an env (smoke tests, browser open).",
		prerequisites: []string{prereqAzureAuth, prereqAppLive},
		dependencies:  []string{depAzureARM},
	},
	"lazure scale": {
		useCase:       "change replica min/max bounds without editing and re-deploying the manifest.",
		prerequisites: []string{prereqAzureAuth, prereqAppLive},
		dependencies:  []string{depAzureARM},
	},
	"lazure events": {
		useCase:       "investigate ARM-level activity (deployments, scale events, failures) over a time window.",
		prerequisites: []string{prereqAzureAuth, prereqAppLive},
		dependencies:  []string{depAzureARM},
	},
	"lazure validate": {
		useCase:       "static pre-flight before deploy — catches manifest, vars, template and secret-reference errors with no Azure calls.",
		prerequisites: []string{prereqManifest},
	},
	"lazure rollback": {
		useCase:       "shift traffic back to a previous revision when the latest deploy is bad.",
		prerequisites: []string{prereqAzureAuth, prereqAppLive, "Target revision must still exist (Container Apps prunes old revisions automatically)."},
		dependencies:  []string{depAzureARM},
	},
	"lazure restart": {
		useCase:       "force replicas to restart (e.g. to pick up new secrets injected at startup).",
		prerequisites: []string{prereqAzureAuth, prereqAppLive},
		dependencies:  []string{depAzureARM},
	},
	"lazure exec": {
		useCase:       "open a shell or run a one-off command inside a running container.",
		prerequisites: []string{prereqAzureAuth, prereqAppLive},
		dependencies:  []string{depAzCLI},
	},
	"lazure doctor": {
		useCase:      "pre-flight your local toolchain (az login, sops, docker, etc.) before running anything destructive.",
		dependencies: []string{"Probes for `az`, `docker`, `sops`, `git` on PATH; reports what's missing."},
	},

	// ---------- Onboarding ----------
	"lazure init": {
		useCase:       "scaffold deploy.yml, envs/ and .gitignore in a fresh repo.",
		prerequisites: []string{"Writable current working directory."},
	},
	"lazure env": {
		useCase: "inspect or compare environments — list available envs, or diff vars/secrets across them.",
	},
	"lazure env list": {
		useCase: "list every environment lazure knows about (one per line, script-friendly).",
	},
	"lazure env diff": {
		useCase:       "spot asymmetries — vars/secrets that differ across envs, orphans, or missing entries the next deploy would fail on.",
		prerequisites: []string{prereqSops, "All `envs/<env>.secrets.yml` files exist (one per env)."},
		dependencies:  []string{depSops},
	},
	"lazure schema": {
		useCase: "emit the JSON Schema for deploy.yml so editors/CI can validate it.",
	},

	// ---------- Config surface ----------
	"lazure secrets": {
		useCase:       "manage SOPS-encrypted secrets per environment (view, edit, sync to Key Vault).",
		prerequisites: []string{prereqSops},
		dependencies:  []string{depSops},
	},
	"lazure secrets new": {
		useCase:       "create an empty encrypted secrets file for a new environment.",
		prerequisites: []string{prereqSops},
		dependencies:  []string{depSops},
	},
	"lazure secrets view": {
		useCase:       "read encrypted secret values for an env (redacted by default; `--reveal` to show).",
		prerequisites: []string{prereqSops, "`envs/<env>.secrets.yml` exists."},
		dependencies:  []string{depSops},
	},
	"lazure secrets edit": {
		useCase:       "edit encrypted secrets — decrypts to a sidecar file, opens `$EDITOR`, re-encrypts on save.",
		prerequisites: []string{prereqSops, prereqEditor, "No stale `envs/<env>.secrets.plain.yml` left over from a prior interrupted edit."},
		dependencies:  []string{depSops},
	},
	"lazure secrets export": {
		useCase:       "print `export KEY=VAL` lines you can `eval` to mirror the container's env locally (only env vars referenced in deploy.yml).",
		prerequisites: []string{prereqSops, prereqManifest},
		dependencies:  []string{depSops},
	},
	"lazure secrets decrypt": {
		useCase:       "write the decrypted plaintext sidecar to disk for manual editing.",
		prerequisites: []string{prereqSops},
		dependencies:  []string{depSops},
	},
	"lazure secrets encrypt": {
		useCase:       "re-encrypt a manually edited plaintext sidecar back to the SOPS file.",
		prerequisites: []string{prereqSops, "`envs/<env>.secrets.plain.yml` exists."},
		dependencies:  []string{depSops},
	},
	"lazure secrets verify": {
		useCase:       "confirm every `{secret: X}` reference in the manifest is actually present in the SOPS file (and optionally in Key Vault).",
		prerequisites: []string{prereqSops, prereqManifest, "With `--check-kv`: " + prereqAzureAuth},
		dependencies:  []string{depSops, "With `--check-kv`: " + depAzureARM},
	},
	"lazure secrets sync": {
		useCase:       "push all SOPS secrets up to Azure Key Vault (where Container Apps reads them).",
		prerequisites: []string{prereqSops, prereqAzureAuth, "Caller has `set` permission on the target Key Vault."},
		dependencies:  []string{depSops, depAzureARM},
	},
	"lazure vars": {
		useCase: "manage plain-text vars files per environment (view, edit, verify).",
	},
	"lazure vars view": {
		useCase:       "read effective vars for an env (std_vars + envs/<env>.vars.yml + any `--var` overrides).",
		prerequisites: []string{prereqManifest},
	},
	"lazure vars edit": {
		useCase:       "open the plaintext envs/<env>.vars.yml in `$EDITOR` (creates a stub if missing).",
		prerequisites: []string{prereqEditor},
	},
	"lazure vars verify": {
		useCase:       "load + render the manifest with the env's vars; surfaces YAML, template and validation errors.",
		prerequisites: []string{prereqManifest},
	},
	"lazure vars export": {
		useCase:       "print plain-string env vars from deploy.yml as `export KEY=VAL` lines.",
		prerequisites: []string{prereqManifest},
	},

	// ---------- Documentation ----------
	"lazure llm": {
		useCase: "produce an LLM-friendly reference of every command, flag, prerequisite and use case (this document).",
	},
}

// optionalSingularArgs lists path:argName pairs where a singular
// `*cli.StringArg` is actually optional (parser-wise it's always
// "0 or 1", but lazure's actions usually enforce required — schema
// is the exception).
var optionalSingularArgs = map[string]map[string]bool{
	"lazure schema": {"path": true},
}

// ---------------------------- builders -----------------------------

func buildDoc(root *cli.Command) Doc {
	d := Doc{
		Name:       root.Name,
		Usage:      root.Usage,
		Invocation: fmt.Sprintf("%s [global-flags] <command> [args] [flags]", root.Name),
		Notes: []string{
			"Commands that act on a deployed app accept an `<env>` positional argument naming the target environment (e.g. `dev`, `uat`, `prd`).",
			"Each environment maps to `deploy/envs/<env>.vars.yml` for plain-text vars and `deploy/envs/<env>.secrets.yml` for SOPS-encrypted secrets.",
			"Required arguments and flags are marked `(required)`. Variadic positional args end with `...`.",
		},
		GlobalFlags: collectFlags(visibleFlags(root.Flags)),
	}

	visible := make([]*cli.Command, 0, len(root.Commands))
	for _, c := range root.Commands {
		if c.Name == "help" || c.Name == "h" || c.Name == "completion" {
			continue
		}
		visible = append(visible, c)
	}

	for _, group := range groupByCategory(visible) {
		cat := CategoryDoc{Name: group.name}
		for _, sub := range group.cmds {
			cat.Commands = append(cat.Commands, buildCommand([]string{root.Name}, sub))
		}
		d.Categories = append(d.Categories, cat)
	}
	return d
}

type cmdGroup struct {
	name string
	cmds []*cli.Command
}

func groupByCategory(cmds []*cli.Command) []cmdGroup {
	buckets := map[string][]*cli.Command{}
	for _, c := range cmds {
		cat := commandCategory[c.Name]
		if cat == "" {
			cat = "Other"
		}
		buckets[cat] = append(buckets[cat], c)
	}
	for _, list := range buckets {
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	}
	seen := map[string]bool{}
	var out []cmdGroup
	for _, name := range categoryOrder {
		if list, ok := buckets[name]; ok {
			out = append(out, cmdGroup{name: name, cmds: list})
			seen[name] = true
		}
	}
	var rest []string
	for name := range buckets {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		out = append(out, cmdGroup{name: name, cmds: buckets[name]})
	}
	return out
}

func buildCommand(parents []string, cmd *cli.Command) CommandDoc {
	full := append([]string{}, parents...)
	full = append(full, cmd.Name)
	pathStr := strings.Join(full, " ")

	doc := CommandDoc{
		Path:      pathStr,
		Name:      cmd.Name,
		Usage:     cmd.Usage,
		Examples:  strings.TrimSpace(cmd.Description),
		Arguments: collectArgs(pathStr, cmd.Arguments),
		Flags:     collectFlags(visibleFlags(cmd.Flags)),
	}
	if meta, ok := commandMetadata[pathStr]; ok {
		doc.UseCase = meta.useCase
		doc.Prerequisites = meta.prerequisites
		doc.Dependencies = meta.dependencies
	}
	for _, sub := range cmd.Commands {
		if sub.Name == "help" || sub.Name == "h" {
			continue
		}
		doc.Subcommands = append(doc.Subcommands, buildCommand(full, sub))
	}
	return doc
}

func collectArgs(path string, args []cli.Argument) []ArgDoc {
	if len(args) == 0 {
		return nil
	}
	out := make([]ArgDoc, 0, len(args))
	for _, a := range args {
		usage := strings.TrimSpace(a.Usage())
		switch v := a.(type) {
		case *cli.StringArg:
			required := true
			if optionalSingularArgs[path][v.Name] {
				required = false
			}
			out = append(out, ArgDoc{
				Name:     v.Name,
				Usage:    fallback(v.UsageText, usage),
				Required: required,
			})
		case *cli.StringArgs:
			variadic := v.Max < 0 || v.Max > 1
			out = append(out, ArgDoc{
				Name:     v.Name,
				Usage:    fallback(v.UsageText, usage),
				Required: v.Min >= 1,
				Variadic: variadic,
			})
		default:
			out = append(out, ArgDoc{Name: "arg", Usage: usage})
		}
	}
	return out
}

// visibleFlags drops flags that urfave/cli auto-injects (--help, --version),
// which apply uniformly and only add noise.
func visibleFlags(flags []cli.Flag) []cli.Flag {
	out := make([]cli.Flag, 0, len(flags))
	for _, f := range flags {
		skip := false
		for _, n := range f.Names() {
			if n == "help" || n == "h" || n == "version" {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, f)
		}
	}
	return out
}

func collectFlags(flags []cli.Flag) []FlagDoc {
	if len(flags) == 0 {
		return nil
	}
	out := make([]FlagDoc, 0, len(flags))
	for _, f := range flags {
		fd := FlagDoc{Names: f.Names()}
		if df, ok := f.(cli.DocGenerationFlag); ok {
			fd.Usage = df.GetUsage()
			fd.Type = df.TypeName()
			fd.EnvVars = df.GetEnvVars()
			if df.IsDefaultVisible() {
				fd.Default = df.GetDefaultText()
			}
		}
		if rf, ok := f.(cli.RequiredFlag); ok && rf.IsRequired() {
			fd.Required = true
		}
		out = append(out, fd)
	}
	return out
}

func fallback(primary, secondary string) string {
	if primary != "" {
		return primary
	}
	return secondary
}

// ---------------------------- emitters -----------------------------

func emitJSON(d Doc) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(d)
}

func emitMarkdown(d Doc) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — CLI reference for LLMs and AI agents\n\n", d.Name)
	if d.Usage != "" {
		fmt.Fprintf(&b, "%s\n\n", d.Usage)
	}
	if d.Invocation != "" {
		fmt.Fprintf(&b, "Invocation: `%s`\n\n", d.Invocation)
	}
	for _, n := range d.Notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	if len(d.Notes) > 0 {
		fmt.Fprintln(&b)
	}
	if len(d.GlobalFlags) > 0 {
		fmt.Fprintln(&b, "## Global flags")
		fmt.Fprintln(&b)
		writeFlagsMD(&b, d.GlobalFlags)
		fmt.Fprintln(&b)
	}
	for _, cat := range d.Categories {
		fmt.Fprintf(&b, "## %s\n\n", cat.Name)
		for _, sub := range cat.Commands {
			writeCommandMD(&b, sub)
		}
	}
	_, err := os.Stdout.WriteString(b.String())
	return err
}

func writeCommandMD(b *strings.Builder, c CommandDoc) {
	fmt.Fprintf(b, "### `%s`\n\n", c.Path)
	if c.Usage != "" {
		fmt.Fprintf(b, "**What it does:** %s\n\n", c.Usage)
	}
	if c.UseCase != "" {
		fmt.Fprintf(b, "**When to use:** %s\n\n", c.UseCase)
	}
	if len(c.Prerequisites) > 0 {
		fmt.Fprintln(b, "**Prerequisites:**")
		fmt.Fprintln(b)
		for _, p := range c.Prerequisites {
			fmt.Fprintf(b, "- %s\n", p)
		}
		fmt.Fprintln(b)
	}
	if len(c.Dependencies) > 0 {
		fmt.Fprintln(b, "**Dependencies:**")
		fmt.Fprintln(b)
		for _, d := range c.Dependencies {
			fmt.Fprintf(b, "- %s\n", d)
		}
		fmt.Fprintln(b)
	}
	if len(c.Arguments) > 0 {
		fmt.Fprintln(b, "**Arguments:**")
		fmt.Fprintln(b)
		for _, a := range c.Arguments {
			writeArgMD(b, a)
		}
		fmt.Fprintln(b)
	}
	if len(c.Flags) > 0 {
		fmt.Fprintln(b, "**Flags:**")
		fmt.Fprintln(b)
		writeFlagsMD(b, c.Flags)
		fmt.Fprintln(b)
	}
	if c.Examples != "" {
		fmt.Fprintln(b, "**Examples / notes:**")
		fmt.Fprintln(b)
		fmt.Fprintln(b, "```")
		fmt.Fprintln(b, c.Examples)
		fmt.Fprintln(b, "```")
		fmt.Fprintln(b)
	}
	for _, sub := range c.Subcommands {
		writeCommandMD(b, sub)
	}
}

func writeArgMD(b *strings.Builder, a ArgDoc) {
	name := a.Name
	if a.Variadic {
		name += "..."
	}
	tags := []string{}
	if a.Required {
		tags = append(tags, "required")
	} else {
		tags = append(tags, "optional")
	}
	if a.Variadic {
		tags = append(tags, "variadic")
	}
	tagStr := ""
	if len(tags) > 0 {
		tagStr = fmt.Sprintf(" *(%s)*", strings.Join(tags, ", "))
	}
	if a.Usage != "" {
		fmt.Fprintf(b, "- `%s`%s — %s\n", name, tagStr, a.Usage)
	} else {
		fmt.Fprintf(b, "- `%s`%s\n", name, tagStr)
	}
}

func writeFlagsMD(b *strings.Builder, flags []FlagDoc) {
	for _, f := range flags {
		writeFlagMD(b, f)
	}
}

func writeFlagMD(b *strings.Builder, f FlagDoc) {
	prefixed := make([]string, 0, len(f.Names))
	for _, n := range f.Names {
		if n == "" {
			continue
		}
		if len(n) == 1 {
			prefixed = append(prefixed, "-"+n)
		} else {
			prefixed = append(prefixed, "--"+n)
		}
	}
	line := strings.Join(prefixed, ", ")
	if f.Required {
		line += " *(required)*"
	}
	if f.Usage != "" {
		line += " — " + f.Usage
	}
	if f.Default != "" {
		line += fmt.Sprintf(" (default: %s)", f.Default)
	}
	if len(f.EnvVars) > 0 {
		line += fmt.Sprintf(" [env: %s]", strings.Join(f.EnvVars, ", "))
	}
	fmt.Fprintf(b, "- `%s`\n", line)
}
