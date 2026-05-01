package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
	"github.com/investerra/lazure/internal/sopsio"
)

// EnvDiff implements `lazure env diff` — a cross-environment matrix
// showing which template-vars are defined where, and which secret
// references in deploy.yml have a corresponding entry in each env's
// SOPS file.
//
// Catches a real bug class: "I added DATABASE_URL_HOST to dev/uat
// but forgot prd" or "I added a secret ref but didn't update the
// prd SOPS file". A 30-second scan answers that question.
//
// Marks:
//   ✓        defined / present
//   ✗        referenced but missing  (red flag — manifest will fail
//            at deploy time)
//   —        not referenced in this env
//
// No Azure / KV calls; only local files.
func EnvDiff(ctx context.Context, c *cli.Command) error {
	dir := c.String("dir")
	envs := discoverEnvs(dir)
	if len(envs) == 0 {
		return errs.Usage(errs.Errorf("env diff: no envs found under %s/envs/", dir))
	}
	if len(envs) < 2 {
		fmt.Println("only one env detected — nothing to diff")
		return nil
	}
	slog.Debug("env diff: scanning", "envs", envs)

	rows, err := buildEnvDiff(ctx, dir, envs)
	if err != nil {
		return err
	}
	return printEnvDiff(rows, envs)
}

// envDiffRow is one line in the matrix — either a var name or a
// secret-ref name, with per-env presence marks. section is "vars" or
// "secrets" so the renderer can group them.
type envDiffRow struct {
	section string
	name    string
	marks   map[string]string // env → mark
}

// buildEnvDiff loads every env's vars + secrets and assembles the
// union of var keys + secret references with per-env presence.
//
// Skips the heavy lazurecfg.LoadManifest path because that demands a
// fully-set-up env (rendered template, decrypted secrets, vault URL
// resolved) — env diff is supposed to work BEFORE everything's wired
// up, surfacing exactly that kind of "this env isn't ready yet" gap.
//
// vars.yml is parsed as raw YAML; the user-facing keys are what we
// need, and template values like `{{ .Vars.X }}` come through as
// literal strings — fine for set-membership questions.
//
// Secret refs come from a single deploy.yml parse (the manifest
// shape is env-invariant — the {secret: name} shape sits in the
// template before render). For per-env divergence in refs (rare —
// only happens if a template uses {{ if eq .Vars.app_env "prd" }})
// we'd need to render per env, but that's an explicit "render"
// command's job; env diff is a static-shape audit.
func buildEnvDiff(ctx context.Context, dir string, envs []string) ([]envDiffRow, error) {
	_ = ctx
	type envState struct {
		vars     map[string]any
		sopsKeys map[string]struct{} // secret names defined in SOPS file
	}
	state := make(map[string]*envState, len(envs))

	// Project-wide shared vars + secrets — keys defined here appear in
	// every env's effective set, so the matrix would mark them ✓ in
	// every column. Filter them out the same way std vars are
	// filtered, to keep signal density high.
	sharedVars := map[string]struct{}{}
	sharedVarsPath := filepath.Join(dir, lazurecfg.SharedVarsFile)
	if raw, err := os.ReadFile(sharedVarsPath); err == nil {
		var v map[string]any
		if err := yaml.Unmarshal(raw, &v); err == nil {
			for k := range v {
				sharedVars[k] = struct{}{}
			}
		} else {
			slog.Warn("env diff: shared vars parse failed", "err", err)
		}
	}
	sharedSecretKeys := map[string]struct{}{}
	sharedSecretsPath := lazurecfg.SharedSecretsPath(dir)
	if _, err := os.Stat(sharedSecretsPath); err == nil {
		shared, err := sopsio.Decrypt(sharedSecretsPath)
		if err != nil {
			slog.Warn("env diff: shared secrets decrypt failed", "err", err)
		} else {
			for k := range shared {
				sharedSecretKeys[k] = struct{}{}
			}
		}
	}

	// Per-env vars + secrets (raw file reads — no template render, so
	// env diff still works on projects whose templates can't render
	// yet, exactly the asymmetry-spotting use case the command exists
	// for).
	for _, env := range envs {
		es := &envState{}
		state[env] = es

		varsPath := filepath.Join(dir, "envs", env+".vars.yml")
		if raw, err := os.ReadFile(varsPath); err == nil {
			var v map[string]any
			if err := yaml.Unmarshal(raw, &v); err != nil {
				slog.Warn("env diff: vars parse failed", "env", env, "err", err)
			} else {
				es.vars = v
			}
		} else {
			slog.Warn("env diff: vars.yml not found", "env", env, "path", varsPath)
		}

		secretsPath := lazurecfg.EnvSecretsPath(dir, env)
		if _, err := os.Stat(secretsPath); err == nil {
			secrets, err := sopsio.Decrypt(secretsPath)
			if err != nil {
				slog.Warn("env diff: secrets decrypt failed", "env", env, "err", err)
			} else {
				es.sopsKeys = make(map[string]struct{}, len(secrets))
				for k := range secrets {
					es.sopsKeys[k] = struct{}{}
				}
			}
		}
	}

	// Secret refs come from a single deploy.yml parse — manifest shape
	// is env-invariant in the common case. Tolerate parse failures
	// (template render not yet possible, schema mismatch) by warning
	// and continuing with no refs; the matrix still shows var
	// asymmetry, just not secret-reference status.
	manifestRefs := map[string]struct{}{}
	manifestPath := filepath.Join(dir, "deploy.yml")
	if raw, err := os.ReadFile(manifestPath); err == nil {
		var m lazurecfg.Manifest
		if err := yaml.Unmarshal(raw, &m); err == nil {
			for _, name := range lazurecfg.CollectSecretRefs(&m) {
				manifestRefs[name] = struct{}{}
			}
		} else {
			slog.Warn("env diff: deploy.yml parse failed; secret refs will not be checked", "err", err)
		}
	} else {
		slog.Warn("env diff: deploy.yml not found", "path", manifestPath)
	}

	// Build union of var keys across envs, ignoring std vars (auto-
	// injected) AND shared vars (uniformly defined for every env via
	// vars.yml). Both categories would show ✓ in every column — pure
	// noise.
	std := stdVarsSet()
	allVars := map[string]struct{}{}
	for _, env := range envs {
		for k := range state[env].vars {
			if _, isStd := std[k]; isStd {
				continue
			}
			if _, isShared := sharedVars[k]; isShared {
				continue
			}
			allVars[k] = struct{}{}
		}
	}

	// Union of secret keys across (manifest refs ∪ every env's SOPS
	// keys), minus shared SOPS keys for the same noise-reduction
	// reason. Surfaces both "missing in this env" and "orphan in this
	// env" cases.
	allSecrets := map[string]struct{}{}
	for k := range manifestRefs {
		if _, isShared := sharedSecretKeys[k]; !isShared {
			allSecrets[k] = struct{}{}
		}
	}
	for _, env := range envs {
		for k := range state[env].sopsKeys {
			if _, isShared := sharedSecretKeys[k]; !isShared {
				allSecrets[k] = struct{}{}
			}
		}
	}

	var rows []envDiffRow
	for _, k := range sortedKeysSet(allVars) {
		row := envDiffRow{section: "vars", name: k, marks: map[string]string{}}
		for _, env := range envs {
			if _, ok := state[env].vars[k]; ok {
				row.marks[env] = "✓"
			} else {
				row.marks[env] = "✗"
			}
		}
		rows = append(rows, row)
	}
	for _, k := range sortedKeysSet(allSecrets) {
		row := envDiffRow{section: "secrets", name: k, marks: map[string]string{}}
		_, refed := manifestRefs[k]
		for _, env := range envs {
			_, present := state[env].sopsKeys[k]
			switch {
			case refed && present:
				row.marks[env] = "✓"
			case refed && !present:
				row.marks[env] = "✗" // referenced but missing — deploy will fail
			case !refed && present:
				row.marks[env] = "○" // present but not referenced — orphan
			default:
				row.marks[env] = "—"
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// printEnvDiff writes the matrix as a tab-aligned grouped table.
func printEnvDiff(rows []envDiffRow, envs []string) error {
	if len(rows) == 0 {
		fmt.Println("no vars or secret refs to compare")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	// Header: one column per env.
	header := "NAME"
	for _, e := range envs {
		header += "\t" + e
	}
	fmt.Fprintln(tw, header)

	currentSection := ""
	for _, row := range rows {
		if row.section != currentSection {
			fmt.Fprintf(tw, "\n%s:\t\n", row.section)
			currentSection = row.section
		}
		line := "  " + row.name
		for _, e := range envs {
			line += "\t" + row.marks[e]
		}
		fmt.Fprintln(tw, line)
	}
	if err := tw.Flush(); err != nil {
		return errs.System(errs.Wrap(err, "env diff: flush"))
	}

	fmt.Println()
	fmt.Println("legend: ✓ defined  ✗ referenced but missing  ○ defined but not referenced  — n/a")
	return nil
}

// stdVarsSet returns the set of vars lazure injects automatically
// (git metadata, app_env, keyvault_url). These are uniform across
// envs and don't belong in the diff — they'd just add noise rows
// where every cell is ✓.
func stdVarsSet() map[string]struct{} {
	return map[string]struct{}{
		"app_env":          {},
		"keyvault_url":     {},
		"git_branch":       {},
		"git_commit":       {},
		"git_short_commit": {},
		"git_dirty":        {},
	}
}

// sortedKeysSet returns the keys of a string-keyed set in alphabetical
// order. tabular output stays stable across runs; grep-friendliness
// stays high.
func sortedKeysSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

