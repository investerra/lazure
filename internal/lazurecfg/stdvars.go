package lazurecfg

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// StandardVars assembles the variables auto-injected into the template
// rendering context: app_env, keyvault_url (from the SOPS metadata of the
// env's secrets file when present), and git_branch / git_commit /
// git_short_commit / git_dirty.
//
// All inputs are optional. A missing secrets file resolves keyvault_url
// to an empty string — users who need the vault URL for templates can
// override it via vars.yml, env vars file, or `--var keyvault_url=...`.
// Commands that actually require a real Key Vault (secrets sync, deploy
// when manifest references secrets) error at the use site, not at load.
// Git failures are soft: WARN logged, git_* vars set to empty strings
// so templates still render.
//
// If the working tree is dirty, git_short_commit carries a "-dirty" suffix
// so downstream image tags surface the uncommitted state.
func StandardVars(projectDir, env string) (map[string]any, error) {
	vars := map[string]any{
		"app_env":      env,
		"keyvault_url": "",
	}

	url, err := LoadVaultURL(LoadOptions{ProjectDir: projectDir, Env: env})
	if err != nil {
		return nil, fmt.Errorf("stdvars: %w", err)
	}
	vars["keyvault_url"] = url
	if url == "" {
		slog.Debug("stdvars: no secrets file; keyvault_url defaults to empty",
			"dir", projectDir, "env", env)
	}

	addGitVars(vars, projectDir)
	return vars, nil
}

func addGitVars(vars map[string]any, dir string) {
	if _, err := gitOutput(dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		slog.Warn("stdvars: not a git repository; git_* vars will be empty", "dir", dir)
		vars["git_branch"] = ""
		vars["git_commit"] = ""
		vars["git_short_commit"] = ""
		vars["git_dirty"] = false
		return
	}

	branch, _ := gitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
	commit, _ := gitOutput(dir, "rev-parse", "HEAD")
	short, _ := gitOutput(dir, "rev-parse", "--short", "HEAD")
	dirty := gitIsDirty(dir)
	if dirty {
		short += "-dirty"
	}

	vars["git_branch"] = branch
	vars["git_commit"] = commit
	vars["git_short_commit"] = short
	vars["git_dirty"] = dirty
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitIsDirty(dir string) bool {
	out, err := gitOutput(dir, "status", "--porcelain")
	if err != nil {
		return false
	}
	return out != ""
}
