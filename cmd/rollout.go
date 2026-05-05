package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
)

func RolloutFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "no-build", Usage: "skip docker build and push"},
		&cli.BoolFlag{Name: "no-tag", Usage: "skip creating the calver git tag"},
		&cli.BoolFlag{Name: "no-push", Usage: "skip pushing branch and tag to origin"},
		&cli.BoolFlag{Name: "no-secret-sync", Usage: "skip syncing SOPS secrets to Key Vault"},
		&cli.BoolFlag{Name: "no-version-wait", Usage: "skip public /version verification after deploy"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt"},
		&cli.BoolFlag{Name: "dry-run", Usage: "print the rollout plan without changing anything"},
	}
}

func Rollout(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("rollout: env argument is required (e.g. 'lazure rollout uat')"))
	}
	dir := c.String("dir")
	yes := c.Bool("yes")
	dryRun := c.Bool("dry-run")
	noBuild := c.Bool("no-build")
	noTag := c.Bool("no-tag")
	noPush := c.Bool("no-push")
	noSecretSync := c.Bool("no-secret-sync")
	noVersionWait := c.Bool("no-version-wait")

	slog.Debug("rollout: start", "env", env, "dir", dir,
		"no_build", noBuild, "no_tag", noTag, "no_push", noPush, "no_secret_sync", noSecretSync)

	if out, err := gitRun(ctx, "status", "--porcelain"); err != nil {
		return errs.System(errs.Wrap(err, "rollout: git status"))
	} else if err := rolloutCleanTreeError(out); err != nil {
		return errs.Usage(err)
	}

	sha, err := gitRun(ctx, "rev-parse", "HEAD")
	if err != nil {
		return errs.System(errs.Wrap(err, "rollout: git rev-parse"))
	}
	sha = strings.TrimSpace(sha)

	branch, err := gitRun(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return errs.System(errs.Wrap(err, "rollout: git branch"))
	}
	branch = strings.TrimSpace(branch)
	if branch == "HEAD" && !noPush {
		return errs.Usage(errs.New("rollout: detached HEAD cannot be pushed; checkout a branch or use --no-push"))
	}

	slog.Info("fetching tags from origin")
	if _, err := gitRun(ctx, "fetch", "origin", "--tags"); err != nil {
		return errs.System(errs.Wrap(err, "rollout: git fetch tags"))
	}
	tags, err := listAllTags(ctx)
	if err != nil {
		return errs.System(errs.Wrap(err, "rollout: list tags"))
	}
	nextTag := computeNextTag(time.Now().UTC(), tags)
	baseTag, err := describeLatestTag(ctx)
	if err != nil {
		return errs.System(errs.Wrap(err, "rollout: describe base tag"))
	}
	changelog, err := buildChangelog(ctx, baseTag)
	if err != nil {
		return errs.System(errs.Wrap(err, "rollout: changelog"))
	}

	preview := renderRolloutPreview(rolloutPlan{
		Env: env, Dir: dir, Branch: branch, SHA: sha, BaseTag: baseTag, Tag: nextTag,
		Changelog: changelog, NoBuild: noBuild, NoTag: noTag, NoPush: noPush,
		NoSecretSync: noSecretSync, NoVersionWait: noVersionWait,
	})
	fmt.Print(preview)
	if dryRun {
		slog.Info("dry-run — nothing changed")
		return nil
	}
	if !yes && !promptConfirm("proceed?") {
		return errs.Usage(errs.New("rollout: aborted by user"))
	}

	if !noBuild {
		vars, err := lazurecfg.LoadVars(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
		if err != nil {
			return errs.Usage(errs.Wrap(err, "rollout: load vars"))
		}
		if err := runImageBuild(ctx, imageBuildOptions{
			Env:        env,
			ProjectDir: dir,
			Vars:       vars,
			Push:       true,
			Pull:       true,
		}); err != nil {
			return errs.System(errs.Wrap(err, "rollout: build"))
		}
	}

	if !noSecretSync {
		if err := runSecretsSync(ctx, dir, env); err != nil {
			return errs.System(errs.Wrap(err, "rollout: secrets sync"))
		}
	}

	if !noTag {
		slog.Info("creating annotated tag", "tag", nextTag)
		if _, err := gitRun(ctx, "tag", "-a", nextTag, "-m", composeTagBody("", changelog)); err != nil {
			return errs.System(errs.Wrap(err, "rollout: git tag"))
		}
	}

	if !noPush {
		slog.Info("pushing branch", "branch", branch)
		if _, err := gitRun(ctx, "push", "origin", branch); err != nil {
			return errs.System(errs.Wrap(err, "rollout: git push branch"))
		}
		if !noTag {
			slog.Info("pushing tag", "tag", nextTag)
			if _, err := gitRun(ctx, "push", "origin", nextTag); err != nil {
				return errs.System(errs.Wrap(err, "rollout: git push tag"))
			}
		}
	}

	if err := runSelf(ctx, "--dir", dir, "deploy", env, "-y", "--wait", "--logs"); err != nil {
		return errs.System(errs.Wrap(err, "rollout: deploy"))
	}
	if !noVersionWait {
		if err := runSelf(ctx, "--dir", dir, "wait-for-deploy", env, "--expected-sha", sha); err != nil {
			return errs.System(errs.Wrap(err, "rollout: wait-for-deploy"))
		}
	}
	return nil
}

func rolloutCleanTreeError(status string) error {
	if strings.TrimSpace(status) == "" {
		return nil
	}
	return errs.Errorf("rollout: working tree is not clean; commit or stash changes before rollout:\n%s", strings.TrimRight(status, "\n"))
}

type rolloutPlan struct {
	Env, Dir, Branch, SHA, BaseTag, Tag, Changelog      string
	NoBuild, NoTag, NoPush, NoSecretSync, NoVersionWait bool
}

func renderRolloutPreview(p rolloutPlan) string {
	var b strings.Builder
	b.WriteString("\nrollout plan:\n")
	b.WriteString("  env:      " + p.Env + "\n")
	b.WriteString("  branch:   " + p.Branch + "\n")
	b.WriteString("  commit:   " + shortSHA(p.SHA) + "\n")
	if p.NoTag {
		b.WriteString("  tag:      (skipped)\n")
	} else {
		b.WriteString("  tag:      " + p.Tag + "\n")
	}
	if p.BaseTag == "" {
		b.WriteString("  since:    (first rollout — no prior calver tag)\n")
	} else {
		b.WriteString("  since:    " + p.BaseTag + "\n")
	}
	b.WriteString(fmt.Sprintf("  build:    %s\n", enabledWord(!p.NoBuild)))
	b.WriteString(fmt.Sprintf("  secrets:  %s\n", enabledWord(!p.NoSecretSync)))
	b.WriteString(fmt.Sprintf("  push:     %s\n", enabledWord(!p.NoPush)))
	b.WriteString(fmt.Sprintf("  verify:   %s\n", enabledWord(!p.NoVersionWait)))
	b.WriteString("\nchangelog:\n")
	for line := range strings.SplitSeq(p.Changelog, "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

func enabledWord(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// runSecretsSync invokes `lazure secrets sync <env> -y` as a child
// process, the way both `rollout` and `deploy --sync` do it. Centralized
// so the args list stays in one place.
func runSecretsSync(ctx context.Context, dir, env string) error {
	return runSelf(ctx, "--dir", dir, "secrets", "sync", env, "-y")
}

func runSelf(ctx context.Context, args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return runStreamed(ctx, exe, args...)
}
