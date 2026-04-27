package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
	"github.com/investerra/lazure/internal/sopsio"
)

// SecretsExport implements `lazure secrets export <env>`. Emits
// `export KEY='value'` lines for every entry in deploy.yml's
// resolved env block whose value is a SecretRef, with the secret
// name resolved to the decrypted SOPS value. Designed for shell
// sourcing via `eval $(lazure secrets export dev)` so the local
// shell ends up with the same KEY → secret mapping the deployed
// container would receive.
//
// Variables defined in deploy.yml as plain strings are skipped
// here — use `lazure vars export` for those.
func SecretsExport(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	containerName := c.String("container")
	slog.Debug("secrets export: start", "env", env, "container", containerName)

	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: c.String("dir"), Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets export: load manifest"))
	}
	secrets, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets export: decrypt"))
	}

	resolvedEnv, err := resolveContainerEnv(manifest, containerName)
	if err != nil {
		return errs.Usage(err)
	}

	for _, k := range sortedEnvKeys(resolvedEnv) {
		v := resolvedEnv[k]
		if !v.IsSecret() {
			continue
		}
		value, ok := secrets[v.SecretRef]
		if !ok {
			return errs.Validation(errs.Errorf(
				"secrets export: %s references secret %q which is not in SOPS",
				k, v.SecretRef))
		}
		fmt.Println(formatExport(k, value))
	}
	return nil
}

// VarsExport implements `lazure vars export <env>`. Emits
// `export KEY='value'` lines for every entry in deploy.yml's
// resolved env block whose value is a plain string (post-template
// rendering). Secret refs are skipped — use `lazure secrets export`
// for those.
func VarsExport(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("env argument is required (e.g. 'lazure vars export dev')"))
	}
	containerName := c.String("container")
	slog.Debug("vars export: start", "env", env, "container", containerName)

	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: c.String("dir"), Env: env})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "vars export: load manifest"))
	}

	resolvedEnv, err := resolveContainerEnv(manifest, containerName)
	if err != nil {
		return errs.Usage(err)
	}

	for _, k := range sortedEnvKeys(resolvedEnv) {
		v := resolvedEnv[k]
		if v.IsSecret() {
			continue
		}
		fmt.Println(formatExport(k, v.Value))
	}
	return nil
}

// resolveContainerEnv picks the named container (or the first
// regular container if name is empty) and returns its resolved env
// — shared ⊕ per-container env|merge_env.
func resolveContainerEnv(m *lazurecfg.Manifest, name string) (map[string]*lazurecfg.EnvValue, error) {
	container, err := pickManifestContainer(m, name)
	if err != nil {
		return nil, err
	}
	return lazurecfg.ResolveEnv(m.Env, container)
}

// pickManifestContainer matches by name across regular and init
// containers, or defaults to the first regular container (or first
// init container if no regulars exist). Errors if the manifest has
// no containers at all or the named one isn't found, listing what
// is available in the error.
func pickManifestContainer(m *lazurecfg.Manifest, name string) (*lazurecfg.Container, error) {
	if name != "" {
		for i := range m.Containers {
			if m.Containers[i].Name == name {
				return &m.Containers[i], nil
			}
		}
		for i := range m.InitContainers {
			if m.InitContainers[i].Name == name {
				return &m.InitContainers[i], nil
			}
		}
		return nil, errs.Errorf("container %q not found; available: %s",
			name, strings.Join(availableContainerNames(m), ", "))
	}
	if len(m.Containers) > 0 {
		return &m.Containers[0], nil
	}
	if len(m.InitContainers) > 0 {
		return &m.InitContainers[0], nil
	}
	return nil, errs.New("manifest has no containers")
}

func availableContainerNames(m *lazurecfg.Manifest) []string {
	names := make([]string, 0, len(m.Containers)+len(m.InitContainers))
	for _, c := range m.Containers {
		names = append(names, c.Name)
	}
	for _, c := range m.InitContainers {
		names = append(names, c.Name)
	}
	return names
}

func sortedEnvKeys(env map[string]*lazurecfg.EnvValue) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// formatExport writes a POSIX-portable single-quoted export. The
// `'\''` idiom (close, escaped literal apostrophe, reopen) handles
// embedded single quotes safely. Output is `eval`-safe in any sh /
// bash / zsh / dash.
func formatExport(key, value string) string {
	return fmt.Sprintf("export %s='%s'", key, strings.ReplaceAll(value, "'", `'\''`))
}
