package lazurecfg

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

// LoadVars assembles the final rendering-context Vars map:
//
//  1. Standard vars (app_env, keyvault_url, git_*) from StandardVars.
//  2. If envs/{env}.vars.yml exists: render it as a Go template against
//     the standard vars only, parse the YAML result, and merge on top.
//  3. Apply CLI --var overrides on top of that.
//
// Vars-file keys override standard vars; CLI overrides win over everything.
// User vars cannot reference other user vars through the template because
// vars.yml is rendered before they exist in scope — only standard vars are
// available at that point.
func LoadVars(opts LoadOptions) (map[string]any, error) {
	vars, err := StandardVars(opts.ProjectDir, opts.Env)
	if err != nil {
		return nil, err
	}

	varsPath := filepath.Join(opts.ProjectDir, "envs", opts.Env+".vars.yml")
	if _, err := os.Stat(varsPath); err == nil {
		rendered, err := renderTemplate(varsPath, vars)
		if err != nil {
			return nil, err
		}
		userVars := map[string]any{}
		if len(bytes.TrimSpace(rendered)) > 0 {
			if err := yaml.Unmarshal(rendered, &userVars); err != nil {
				return nil, fmt.Errorf("parse rendered %s: %w", varsPath, err)
			}
		}
		for k, v := range userVars {
			vars[k] = v
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat %s: %w", varsPath, err)
	}

	for k, v := range opts.CLIVars {
		vars[k] = v
	}

	return vars, nil
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
	rendered, err := renderTemplate(manifestPath, vars)
	if err != nil {
		return nil, nil, err
	}

	var m Manifest
	if err := yaml.Unmarshal(rendered, &m); err != nil {
		return nil, nil, fmt.Errorf("parse rendered deploy.yml: %w", err)
	}
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

// funcMap returns the template function set: sprig (~150 fns) overlaid with
// Helm-style `required`. Registration order — sprig first, then required —
// ensures our overlay wins any future sprig collision.
func funcMap() template.FuncMap {
	fm := sprig.FuncMap()
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
