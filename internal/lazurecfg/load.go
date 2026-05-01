package lazurecfg

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"sigs.k8s.io/yaml"
)

// LoadOptions configures a full manifest load. ProjectDir is the directory
// containing deploy.yml + envs/; Env is the positional environment argument
// (e.g. "dev"); CLIVars holds --var overrides that win over vars.yml.
type LoadOptions struct {
	ProjectDir string
	Env        string
	CLIVars    map[string]string
}

// SharedVarsFile is the conventional name for project-wide vars
// shared across every environment. Lives next to deploy.yml. Optional.
const SharedVarsFile = "vars.yml"

// LoadVars assembles the final rendering-context Vars map by walking
// layered sources, lowest precedence first:
//
//  1. StandardVars: app_env, keyvault_url, git_*.
//  2. <projectDir>/vars.yml (project-wide shared) — optional.
//  3. <projectDir>/envs/<env>.vars.yml (per-env) — optional.
//  4. CLIVars overrides.
//
// Each YAML layer is rendered as a Go template against the merged
// vars from all earlier layers, so a key in the env file can reference
// shared keys, shared keys can reference standard vars, and so on.
// Within a single file, plain-literal keys (no `{{` in the value) are
// also visible to templated keys in the same file via mergeVarsFile's
// two-pass extract — see that function for the precise rules. Two
// templated keys in the same file still cannot reference each other.
//
// Every file is optional: a missing layer is a no-op, not an error.
func LoadVars(opts LoadOptions) (map[string]any, error) {
	slog.Debug("lazurecfg: loading standard vars", "env", opts.Env, "dir", opts.ProjectDir)
	vars, err := StandardVars(opts.ProjectDir, opts.Env)
	if err != nil {
		return nil, err
	}
	slog.Debug("lazurecfg: standard vars ready",
		"app_env", vars["app_env"],
		"keyvault_url", vars["keyvault_url"],
		"git_commit", vars["git_commit"])

	if err := mergeVarsFile(filepath.Join(opts.ProjectDir, SharedVarsFile), vars, "shared"); err != nil {
		return nil, err
	}
	if err := mergeVarsFile(filepath.Join(opts.ProjectDir, "envs", opts.Env+".vars.yml"), vars, "env"); err != nil {
		return nil, err
	}

	if len(opts.CLIVars) > 0 {
		slog.Debug("lazurecfg: applying CLI --var overrides", "count", len(opts.CLIVars))
	}
	for k, v := range opts.CLIVars {
		vars[k] = v
	}

	return vars, nil
}

// mergeVarsFile renders path as a Go template against the current
// vars map and merges the resulting YAML keys on top, mutating vars
// in place. A missing file is a no-op; any other stat / parse /
// template error is propagated. layer is a short label used in slog
// lines so debugging "which file did I just merge" is easy.
//
// Two-pass intra-file resolution: keys with no `{{` in their value
// (plain literals) are extracted from the raw YAML and merged into
// vars BEFORE the template render, so a derived/templated key in the
// same file can reference its sibling literals (e.g.
// `docker_image: '{{ .Vars.acr_server }}/api:{{ .Vars.git_commit }}'`
// next to `acr_server: investerra.azurecr.io`).
//
// Limitation: a templated key cannot reference another templated key
// in the same file — that would require topological resolution. Move
// the dependency to a literal, split into two files, or open an
// issue if the limitation bites.
func mergeVarsFile(path string, vars map[string]any, layer string) error {
	switch _, err := os.Stat(path); {
	case err == nil:
		// fall through
	case errors.Is(err, fs.ErrNotExist):
		slog.Debug("lazurecfg: vars file absent", "layer", layer, "path", path)
		return nil
	default:
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := preMergeLiterals(path, vars); err != nil {
		// Pass-1 parse failures are non-fatal — pass 2 (the real
		// template render + parse) will surface the same error with
		// better positioning. Just log.
		slog.Debug("lazurecfg: pass-1 literal extract skipped", "layer", layer, "path", path, "err", err)
	}

	slog.Debug("lazurecfg: rendering vars file", "layer", layer, "path", path)
	rendered, err := renderTemplate(path, vars)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(rendered)) == 0 {
		slog.Debug("lazurecfg: vars file rendered empty", "layer", layer, "path", path)
		return nil
	}
	merged := map[string]any{}
	if err := yaml.Unmarshal(rendered, &merged); err != nil {
		return fmt.Errorf("parse rendered %s: %w", path, err)
	}
	maps.Copy(vars, merged)
	slog.Debug("lazurecfg: vars file merged", "layer", layer, "path", path, "count", len(merged))
	return nil
}

// preMergeLiterals reads the raw YAML at path (no template render)
// and merges only the plain-literal entries into vars — i.e. values
// whose string form contains no `{{` template marker. This is the
// pass-1 helper for mergeVarsFile's intra-file chaining of literals
// to derived keys.
func preMergeLiterals(path string, vars map[string]any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	rawMap := map[string]any{}
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		return fmt.Errorf("pass-1 parse %s: %w", path, err)
	}
	for k, v := range rawMap {
		if s, ok := v.(string); ok && strings.Contains(s, "{{") {
			continue
		}
		vars[k] = v
	}
	return nil
}

// LoadManifest renders deploy.yml with the full Vars set and unmarshals the
// result into a Manifest. Returns both the manifest and the Vars map used
// to render it (handy for diagnostics).
func LoadManifest(opts LoadOptions) (*Manifest, map[string]any, error) {
	vars, err := LoadVars(opts)
	if err != nil {
		return nil, nil, err
	}

	manifestPath := filepath.Join(opts.ProjectDir, "deploy.yml")
	slog.Debug("lazurecfg: rendering manifest", "path", manifestPath)
	rendered, err := renderTemplate(manifestPath, vars)
	if err != nil {
		return nil, nil, err
	}
	slog.Debug("lazurecfg: manifest rendered", "bytes", len(rendered))

	var m Manifest
	if err := yaml.Unmarshal(rendered, &m); err != nil {
		return nil, nil, fmt.Errorf("parse rendered deploy.yml: %w", err)
	}
	slog.Debug("lazurecfg: manifest parsed",
		"app", m.App.Name,
		"containers", len(m.Containers),
		"init_containers", len(m.InitContainers),
		"env_keys", len(m.Env))
	return &m, vars, nil
}

// renderTemplate reads a template file and executes it with {.Vars: vars}
// as the pipeline context. Template funcs: sprig.FuncMap() + Helm-style
// `required`.
//
// missingkey=zero (matches Helm): a lookup of a missing key returns the
// zero value (nil for map[string]any) instead of erroring. This is what
// lets `{{ .Vars.log_level | default "info" }}` work — the lookup flows
// through to default. Typo detection happens explicitly via `required`,
// not implicitly via lookup errors.
func renderTemplate(path string, vars map[string]any) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	tmpl, err := template.New(filepath.Base(path)).
		Option("missingkey=zero").
		Funcs(funcMap()).
		Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", path, err)
	}

	var buf bytes.Buffer
	ctx := struct{ Vars map[string]any }{Vars: vars}
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return nil, fmt.Errorf("render %s: %w", path, err)
	}
	return buf.Bytes(), nil
}

// funcMap returns the template function set: sprig's HERMETIC funcmap
// (~140 fns minus environment-reading ones) overlaid with Helm-style
// `required`. Registration order — sprig first, then required — ensures
// our overlay wins any future sprig collision.
//
// Hermetic vs full: HermeticTxtFuncMap omits `env`, `expandenv`,
// `getHostByName`, etc. Without that filter, anyone who can write a
// deploy.yml or vars.yml can do `image: "{{ env "GITHUB_TOKEN" }}.x"`
// and exfiltrate process-level secrets via `lazure render` or by
// having them baked into the rendered ARM payload sent to Azure.
// This is the same hardening Helm 3 applied for the same threat.
func funcMap() template.FuncMap {
	fm := sprig.HermeticTxtFuncMap()
	fm["required"] = requiredFunc
	return fm
}

// requiredFunc errors the render if v is nil or an empty string. Mirrors
// Helm's `required "message" value` signature — message first, value
// second — so templates read naturally:
//
//	{{ required "resource_group must be set" .Vars.resource_group }}
func requiredFunc(msg string, v any) (any, error) {
	if v == nil {
		return nil, fmt.Errorf("required: %s", msg)
	}
	if s, ok := v.(string); ok && s == "" {
		return nil, fmt.Errorf("required: %s", msg)
	}
	return v, nil
}
