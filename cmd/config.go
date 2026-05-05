package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
	"github.com/investerra/lazure/internal/verify"
)

// ConfigCommand returns the `lazure config` subcommand: a unified
// read-only view over the env block a container actually sees at
// runtime — plain vars and SOPS-decrypted secrets together, indexed
// by env-var name.
//
// Distinct from `lazure vars` (manage plaintext vars files) and
// `lazure secrets` (manage encrypted secrets files): config is the
// resolved-output surface. Same data those two manage, but joined
// through the manifest's env block and rendered the way the running
// container sees it.
func ConfigCommand() *cli.Command {
	containerFlag := &cli.StringFlag{Name: "container", Usage: "container name (default: first non-init container)"}
	revealFlag := &cli.BoolFlag{Name: "reveal", Usage: "show full secret values (default: redacted/masked)"}
	onlyFlag := &cli.StringFlag{Name: "only", Usage: "filter rows: all|vars|secrets", Value: "all"}

	return &cli.Command{
		Name:  "config",
		Usage: "view the resolved env block (vars + secrets) a container will see",
		Description: `Config commands print the unified env block — plain vars merged with secret references — for one environment, the way the container sees it at runtime.
Secrets are redacted by default; pass --reveal to show full values.`,
		Commands: []*cli.Command{
			{
				Name:      "view",
				Usage:     "print the resolved env (vars + secrets) as a kv grid",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					revealFlag, containerFlag, onlyFlag,
					&cli.StringFlag{Name: "format", Usage: "output format: table|json", Value: "table"},
					&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors (also honored via NO_COLOR env)"},
				},
				Action:        ConfigView,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:      "export",
				Usage:     "print resolved env as `export KEY=VAL` lines (secrets emit '*' unless --reveal)",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					revealFlag, containerFlag, onlyFlag,
					&cli.StringFlag{Name: "match", Usage: "regex (RE2); only emit keys whose name matches"},
				},
				Action:        ConfigExport,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:          "dotenv",
				Usage:         "print resolved env as godotenv-compatible KEY=VAL lines",
				Arguments:     envArgs(),
				Flags:         []cli.Flag{revealFlag, containerFlag, onlyFlag},
				Action:        ConfigDotenv,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:          "json",
				Usage:         "print resolved env as a flat JSON object",
				Arguments:     envArgs(),
				Flags:         []cli.Flag{revealFlag, containerFlag, onlyFlag},
				Action:        ConfigJSON,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:          "keys",
				Usage:         "print resolved env keys, one per line",
				Arguments:     envArgs(),
				Flags:         []cli.Flag{containerFlag, onlyFlag},
				Action:        ConfigKeys,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:  "get",
				Usage: "print one resolved value (errors if the key is missing)",
				Arguments: []cli.Argument{
					&cli.StringArg{Name: "env", UsageText: "target environment (dev|uat|prd|...)"},
					&cli.StringArg{Name: "key", UsageText: "env-var name to fetch"},
				},
				Flags:         []cli.Flag{revealFlag, containerFlag},
				Action:        ConfigGet,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:      "verify",
				Usage:     "validate the manifest + check every secret ref resolves (and optionally exists in Key Vault)",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "check-kv", Usage: "also verify each referenced secret exists in Key Vault"},
				},
				Action:        ConfigVerify,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:  "diff",
				Usage: "compare resolved env between two environments",
				Arguments: []cli.Argument{
					&cli.StringArg{Name: "env_a", UsageText: "first environment"},
					&cli.StringArg{Name: "env_b", UsageText: "second environment"},
				},
				Flags: []cli.Flag{
					revealFlag, containerFlag, onlyFlag,
					&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors (also honored via NO_COLOR env)"},
				},
				Action: ConfigDiff,
			},
		},
	}
}

// resolvedEntry is one row of the unified env view: the env-var name a
// running container sees, plus the source it came from (plain var or
// secret reference).
type resolvedEntry struct {
	Key       string
	Value     string // rendered string for vars; decrypted secret value when reveal=true
	IsSecret  bool
	SecretRef string // only set when IsSecret
	Missing   bool   // true when secret ref is not present in the SOPS file
}

const (
	onlyVarsBit = 1 << iota
	onlySecretsBit
)

// parseOnly turns the --only flag into a bitmask. Returns 0 for unknown
// values so callers can surface a typed usage error.
func parseOnly(s string) int {
	switch s {
	case "", "all":
		return onlyVarsBit | onlySecretsBit
	case "vars":
		return onlyVarsBit
	case "secrets":
		return onlySecretsBit
	default:
		return 0
	}
}

// resolveConfig loads the manifest for env, picks the chosen container,
// and returns the resolved env block as []resolvedEntry. Decrypts the
// SOPS file only when secrets are actually included by the --only
// filter — `--only=vars` paths skip decrypt entirely so they work
// without SOPS keys present.
func resolveConfig(c *cli.Command, env, only string) ([]resolvedEntry, error) {
	dir := c.String("dir")
	containerName := c.String("container")

	want := parseOnly(only)
	if want == 0 {
		return nil, errs.Usage(errs.Errorf("invalid --only %q (want all|vars|secrets)", only))
	}

	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		return nil, errs.Usage(errs.Wrap(err, "config: load manifest"))
	}
	resolvedEnv, err := resolveContainerEnv(manifest, containerName)
	if err != nil {
		return nil, errs.Usage(err)
	}

	var secrets map[string]string
	if want&onlySecretsBit != 0 && hasSecretRefs(resolvedEnv) {
		secrets, err = lazurecfg.LoadSecrets(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
		if err != nil {
			return nil, errs.Usage(errs.Wrap(err, "config: decrypt secrets"))
		}
	}

	out := make([]resolvedEntry, 0, len(resolvedEnv))
	for _, k := range sortedEnvKeys(resolvedEnv) {
		ev := resolvedEnv[k]
		if ev == nil {
			continue
		}
		if ev.IsSecret() {
			if want&onlySecretsBit == 0 {
				continue
			}
			val, ok := secrets[ev.SecretRef]
			out = append(out, resolvedEntry{
				Key: k, IsSecret: true, SecretRef: ev.SecretRef,
				Value: val, Missing: !ok,
			})
		} else {
			if want&onlyVarsBit == 0 {
				continue
			}
			out = append(out, resolvedEntry{Key: k, Value: ev.Value})
		}
	}
	return out, nil
}

func hasSecretRefs(env map[string]*lazurecfg.EnvValue) bool {
	for _, v := range env {
		if v.IsSecret() {
			return true
		}
	}
	return false
}

// ConfigView implements `lazure config view <env>`.
func ConfigView(_ context.Context, c *cli.Command) error {
	op := "config view"
	args, err := parseConfigArgs(c, op)
	if err != nil {
		return err
	}
	format := c.String("format")
	color := shouldColor(c.Bool("no-color"))
	slog.Debug(op+": start", "env", args.env, "reveal", args.reveal, "only", args.only, "format", format, "color", color)

	entries, err := resolveConfig(c, args.env, args.only)
	if err != nil {
		return err
	}

	switch format {
	case "", "table":
		return printConfigTable(os.Stdout, args.env, entries, args.reveal, color)
	case "json":
		return printConfigJSON(entries, args.reveal)
	default:
		return errs.Usage(errs.Errorf("invalid --format %q (want table|json)", format))
	}
}

// configArgs collects the env-scoped flags every config subcommand
// reads. parseConfigArgs validates the env arg, rejects the reserved
// "shared" name where it doesn't make sense, and is the single
// place to grow new shared flags.
type configArgs struct {
	env    string
	reveal bool
	only   string
}

func parseConfigArgs(c *cli.Command, op string) (configArgs, error) {
	env := c.StringArg("env")
	if env == "" {
		return configArgs{}, errs.Usage(errs.Errorf("%s: env argument is required", op))
	}
	return configArgs{
		env:    env,
		reveal: c.Bool("reveal"),
		only:   c.String("only"),
	}, nil
}

func printConfigTable(w io.Writer, env string, entries []resolvedEntry, reveal, color bool) error {
	fmt.Fprintf(w, "# config for %s\n", env)
	rows := [][]string{{"NAME", "VALUE", "SOURCE"}}
	for _, e := range entries {
		var valCell, srcCell string
		switch {
		case e.IsSecret && e.Missing:
			valCell = colorize("<missing>", styleConfigMissing, color)
			srcCell = colorize("secret:"+e.SecretRef, styleConfigMissing, color)
		case e.IsSecret && !reveal:
			valCell = colorize(redact(e.Value), styleConfigRedacted, color)
			srcCell = colorize("secret:"+e.SecretRef, styleConfigSecret, color)
		case e.IsSecret:
			valCell = e.Value
			srcCell = colorize("secret:"+e.SecretRef, styleConfigSecret, color)
		default:
			valCell = e.Value
			srcCell = colorize("var", styleConfigVar, color)
		}
		rows = append(rows, []string{e.Key, valCell, srcCell})
	}
	writeAlignedRows(w, rows)
	return nil
}

func printConfigJSON(entries []resolvedEntry, reveal bool) error {
	type row struct {
		Value     string `json:"value"`
		Source    string `json:"source"`
		SecretRef string `json:"secret_ref,omitempty"`
		Missing   bool   `json:"missing,omitempty"`
	}
	out := make(map[string]row, len(entries))
	for _, e := range entries {
		r := row{Value: e.Value, Source: "var"}
		if e.IsSecret {
			r.Source = "secret"
			r.SecretRef = e.SecretRef
			r.Missing = e.Missing
			if !reveal && !e.Missing {
				r.Value = redact(e.Value)
			}
		}
		out[e.Key] = r
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return errs.System(errs.Wrap(err, "config view: marshal"))
	}
	fmt.Println(string(data))
	return nil
}

// ConfigExport implements `lazure config export <env>`. Without
// --reveal, secret values emit as a literal `'*'` placeholder so the
// output is still eval-safe but obviously non-real. --match takes an
// RE2 regex applied to the env-var key (unanchored): use `^FOO_`
// for prefix matches, `^FOO$` for exact.
func ConfigExport(_ context.Context, c *cli.Command) error {
	const op = "config export"
	args, err := parseConfigArgs(c, op)
	if err != nil {
		return err
	}
	var match *regexp.Regexp
	if pat := c.String("match"); pat != "" {
		match, err = regexp.Compile(pat)
		if err != nil {
			return errs.Usage(errs.Wrap(err, op+": invalid --match regex"))
		}
	}
	entries, err := resolveConfig(c, args.env, args.only)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if match != nil && !match.MatchString(e.Key) {
			continue
		}
		val, err := exportValue(e, args.reveal)
		if err != nil {
			return errs.Validation(errs.Wrap(err, op))
		}
		fmt.Println(formatExport(e.Key, val))
	}
	return nil
}

// ConfigDotenv implements `lazure config dotenv <env>`. Emits
// godotenv-compatible KEY=value lines (no `export`, double-quoted only
// when needed).
func ConfigDotenv(_ context.Context, c *cli.Command) error {
	const op = "config dotenv"
	args, err := parseConfigArgs(c, op)
	if err != nil {
		return err
	}
	entries, err := resolveConfig(c, args.env, args.only)
	if err != nil {
		return err
	}
	for _, e := range entries {
		val, err := exportValue(e, args.reveal)
		if err != nil {
			return errs.Validation(errs.Wrap(err, op))
		}
		fmt.Println(formatDotenv(e.Key, val))
	}
	return nil
}

// ConfigJSON implements `lazure config json <env>`. Flat key→value
// JSON object — the eval-friendly shape for tooling. The richer form
// (with source / secret_ref / missing) is available via
// `config view --format=json`.
func ConfigJSON(_ context.Context, c *cli.Command) error {
	const op = "config json"
	args, err := parseConfigArgs(c, op)
	if err != nil {
		return err
	}
	entries, err := resolveConfig(c, args.env, args.only)
	if err != nil {
		return err
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		val, err := exportValue(e, args.reveal)
		if err != nil {
			return errs.Validation(errs.Wrap(err, op))
		}
		out[e.Key] = val
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return errs.System(errs.Wrap(err, op+": marshal"))
	}
	fmt.Println(string(data))
	return nil
}

// ConfigKeys implements `lazure config keys <env>`.
func ConfigKeys(_ context.Context, c *cli.Command) error {
	args, err := parseConfigArgs(c, "config keys")
	if err != nil {
		return err
	}
	entries, err := resolveConfig(c, args.env, args.only)
	if err != nil {
		return err
	}
	for _, e := range entries {
		fmt.Println(e.Key)
	}
	return nil
}

// ConfigGet implements `lazure config get <env> <key>`. Unlike the
// other subcommands it short-circuits the SOPS decrypt when the
// requested key is a plain var — fetching one var shouldn't need age
// keys on disk.
func ConfigGet(ctx context.Context, c *cli.Command) error {
	_ = ctx
	env := c.StringArg("env")
	key := c.StringArg("key")
	if env == "" || key == "" {
		return errs.Usage(errs.New("config get: env and key arguments are required"))
	}
	reveal := c.Bool("reveal")
	dir := c.String("dir")
	containerName := c.String("container")

	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "config get: load manifest"))
	}
	resolvedEnv, err := resolveContainerEnv(manifest, containerName)
	if err != nil {
		return errs.Usage(err)
	}
	ev, ok := resolvedEnv[key]
	if !ok || ev == nil {
		return errs.Usage(errs.Errorf("config get: %q not found in env %q", key, env))
	}
	if !ev.IsSecret() {
		fmt.Println(ev.Value)
		return nil
	}

	secrets, err := lazurecfg.LoadSecrets(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "config get: decrypt secrets"))
	}
	val, present := secrets[ev.SecretRef]
	if !present {
		return errs.Validation(errs.Errorf(
			"config get: %s references secret %q which is not in SOPS", key, ev.SecretRef))
	}
	if !reveal {
		val = redact(val)
	}
	fmt.Println(val)
	return nil
}

// ConfigVerify implements `lazure config verify <env> [--check-kv]`.
// Combines the structural manifest validation `vars verify` runs with
// the secret-ref ↔ SOPS (and optional Key Vault) cross-check
// `secrets verify` runs — the unified surface check for "is this env
// safe to deploy".
//
// Short-circuits on structural errors, matching the behaviour of
// `secrets verify`: a manifest that fails Validate may not have
// enough shape for CollectSecretRefs to produce useful results, and
// cascading errors out of a broken manifest tend to confuse more than
// they help.
func ConfigVerify(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("config verify: env argument is required"))
	}
	if env == SharedEnvName {
		return errs.Usage(errs.New("config verify: 'shared' is not a valid env for verify (run against a real env name like 'dev' — shared files are verified as part of any env's load)"))
	}
	dir := c.String("dir")
	checkKV := c.Bool("check-kv")
	slog.Debug("config verify: start", "env", env, "dir", dir, "check_kv", checkKV)

	manifest, vars, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{
		ProjectDir: dir,
		Env:        env,
	})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "config verify: load manifest"))
	}

	varsR := verify.Vars(manifest)
	for _, w := range varsR.Warnings {
		slog.Warn(w)
	}
	if varsR.HasErrors() {
		return errs.Validation(errs.Wrap(varsR.Err(), "config verify"))
	}

	decrypted, err := lazurecfg.LoadSecrets(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "config verify: decrypt secrets"))
	}

	var kv verify.KeyVault
	if checkKV {
		tokens, err := azureapi.NewTokenProvider()
		if err != nil {
			return errs.Auth(errs.Wrap(err, "config verify: auth"))
		}
		vaultURL, _ := vars["keyvault_url"].(string)
		kv = azureapi.NewKeyVaultClient(vaultURL, tokens)
	}

	secretsR := verify.Secrets(ctx, manifest, decrypted, kv)
	for _, w := range secretsR.Warnings {
		slog.Warn(w)
	}
	if secretsR.HasErrors() {
		return errs.Validation(secretsR.Err())
	}

	refs := lazurecfg.CollectSecretRefs(manifest)
	slog.Info("config verified", "env", env, "refs", len(refs), "check_kv", checkKV)
	return nil
}

// ConfigDiff implements `lazure config diff <envA> <envB>`.
func ConfigDiff(ctx context.Context, c *cli.Command) error {
	_ = ctx
	envA := c.StringArg("env_a")
	envB := c.StringArg("env_b")
	if envA == "" || envB == "" {
		return errs.Usage(errs.New("config diff: env_a and env_b are required"))
	}
	reveal := c.Bool("reveal")
	only := c.String("only")
	color := shouldColor(c.Bool("no-color"))
	a, err := resolveConfig(c, envA, only)
	if err != nil {
		return err
	}
	b, err := resolveConfig(c, envB, only)
	if err != nil {
		return err
	}
	idxA := indexEntries(a)
	idxB := indexEntries(b)
	keys := unionEntryKeys(idxA, idxB)

	rows := [][]string{{"NAME", envA, envB, "STATUS"}}
	for _, k := range keys {
		ea, okA := idxA[k]
		eb, okB := idxB[k]
		var cellA, cellB, status string
		switch {
		case okA && !okB:
			cellA = formatDiffCell(ea, reveal, color)
			cellB = "—"
			status = colorize("only in "+envA, styleConfigOnlyIn, color)
		case !okA && okB:
			cellA = "—"
			cellB = formatDiffCell(eb, reveal, color)
			status = colorize("only in "+envB, styleConfigOnlyIn, color)
		default:
			cellA = formatDiffCell(ea, reveal, color)
			cellB = formatDiffCell(eb, reveal, color)
			if sameEntry(ea, eb, reveal) {
				status = colorize("same", styleConfigSame, color)
			} else {
				status = colorize("differ", styleConfigDiffer, color)
			}
		}
		rows = append(rows, []string{k, cellA, cellB, status})
	}
	writeAlignedRows(os.Stdout, rows)
	return nil
}

func indexEntries(es []resolvedEntry) map[string]resolvedEntry {
	out := make(map[string]resolvedEntry, len(es))
	for _, e := range es {
		out[e.Key] = e
	}
	return out
}

func unionEntryKeys(a, b map[string]resolvedEntry) []string {
	set := map[string]struct{}{}
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sameEntry reports whether two entries are equivalent for diff
// purposes. Without --reveal, secret values aren't trustworthy
// (they're redacted), so the comparison falls back to ref name +
// missing-status only.
func sameEntry(a, b resolvedEntry, reveal bool) bool {
	if a.IsSecret != b.IsSecret {
		return false
	}
	if a.IsSecret {
		if a.SecretRef != b.SecretRef || a.Missing != b.Missing {
			return false
		}
		if !reveal {
			return true
		}
		return a.Value == b.Value
	}
	return a.Value == b.Value
}

func formatDiffCell(e resolvedEntry, reveal, color bool) string {
	if e.IsSecret {
		switch {
		case e.Missing:
			return colorize("<missing>", styleConfigMissing, color)
		case !reveal:
			return colorize("secret:"+e.SecretRef, styleConfigSecret, color)
		default:
			return e.Value
		}
	}
	return e.Value
}

// writeAlignedRows prints rows aligned by visible width (ANSI-aware
// via lipgloss.Width). Cells may contain ANSI color escape sequences;
// padding is computed against their visible width so columns line up
// correctly regardless of color state.
func writeAlignedRows(w io.Writer, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	cols := len(rows[0])
	widths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row {
			if l := lipgloss.Width(cell); l > widths[i] {
				widths[i] = l
			}
		}
	}
	for _, row := range rows {
		for i, cell := range row {
			pad := max(widths[i]-lipgloss.Width(cell), 0)
			fmt.Fprint(w, cell, strings.Repeat(" ", pad))
			if i < cols-1 {
				fmt.Fprint(w, "  ")
			}
		}
		fmt.Fprintln(w)
	}
}

// Soft palette for config tables, aligned with cmd/diff.go and
// cmd/doctor.go palettes. styleConfigSecret uses soft blue to read
// distinctly from green (vars/same) and coral (failure states).
var (
	styleConfigVar      = lipgloss.NewStyle().Foreground(lipgloss.Color("241")) // dim gray
	styleConfigSecret   = lipgloss.NewStyle().Foreground(lipgloss.Color("110")) // soft blue
	styleConfigRedacted = lipgloss.NewStyle().Foreground(lipgloss.Color("241")) // dim gray
	styleConfigMissing  = lipgloss.NewStyle().Foreground(lipgloss.Color("174")) // soft coral
	styleConfigSame     = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // sage green
	styleConfigDiffer   = lipgloss.NewStyle().Foreground(lipgloss.Color("179")) // muted amber
	styleConfigOnlyIn   = lipgloss.NewStyle().Foreground(lipgloss.Color("174")) // soft coral
)

// exportValue picks the value to print in eval-style outputs. Secrets
// are masked as `*` unless --reveal is set; revealing a missing secret
// returns a typed error so the caller can wrap it with command
// context (e.g. "config export: ..."). Emitting an empty string for a
// missing secret would silently install bad values when the output
// is `eval`'d.
func exportValue(e resolvedEntry, reveal bool) (string, error) {
	if !e.IsSecret {
		return e.Value, nil
	}
	if !reveal {
		return "*", nil
	}
	if e.Missing {
		return "", errs.Errorf("%s references secret %q which is not in SOPS", e.Key, e.SecretRef)
	}
	return e.Value, nil
}

// formatDotenv writes a godotenv-compatible KEY=value line. Simple
// values pass through unquoted; values with whitespace, quotes, or
// shell metacharacters are double-quoted with `\` and `"` escaped.
// Newlines are kept literal inside the double quotes — godotenv's
// multi-line parser accepts this shape.
func formatDotenv(key, value string) string {
	if value == "" {
		return key + `=""`
	}
	if !strings.ContainsAny(value, " \t\n\r\"'\\$#") {
		return key + "=" + value
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return key + `="` + r.Replace(value) + `"`
}
