package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
)

// ReleaseFlags are the flags for `lazure release`.
func ReleaseFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "dry-run", Usage: "print the planned tag + changelog and exit"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt"},
		&cli.StringFlag{Name: "message", Aliases: []string{"m"}, Usage: "optional header line prepended to the changelog"},
		&cli.BoolFlag{Name: "wait", Usage: "after push, watch GitHub Actions runs until they complete (requires gh)"},
	}
}

// Release implements `lazure release`. Repo-level — no env argument.
// Cuts a calver tag v{YYYYMMDD}.{N+1} where N is today's existing max,
// and pushes it to origin. Optional --wait polls gh for workflow runs
// triggered by the push and exits 0/1 based on their conclusion.
//
// Prerequisites (hard errors before any mutation):
//   - current branch is main or master
//   - working tree is pristine (no modified/staged/untracked)
//   - origin has been fetched with tags (we do this)
//
// Shells out to `git` (inherits ssh-agent/credential-helper) and, for
// --wait only, `gh` (inherits gh-auth). No native GitHub API path —
// gh is the tool GitHub ships specifically for this; duplicating it
// here would add dependency + auth surface for marginal benefit.
func Release(ctx context.Context, c *cli.Command) error {
	dryRun := c.Bool("dry-run")
	yes := c.Bool("yes")
	msg := c.String("message")
	wait := c.Bool("wait")
	slog.Debug("release: start", "dry_run", dryRun, "yes", yes, "wait", wait, "has_message", msg != "")

	if err := ensureReleasableBranch(ctx); err != nil {
		return errs.Usage(err)
	}
	if err := ensurePristineTree(ctx); err != nil {
		return errs.Usage(err)
	}

	slog.Info("fetching tags from origin")
	if _, err := gitRun(ctx, "fetch", "origin", "--tags"); err != nil {
		return errs.System(errs.Wrap(err, "release: git fetch"))
	}

	existing, err := listAllTags(ctx)
	if err != nil {
		return errs.System(errs.Wrap(err, "release: list tags"))
	}
	today := time.Now().UTC()
	newTag := computeNextTag(today, existing)
	baseTag, err := describeLatestTag(ctx)
	if err != nil {
		return errs.System(errs.Wrap(err, "release: describe base tag"))
	}

	changelog, err := buildChangelog(ctx, baseTag)
	if err != nil {
		return errs.System(errs.Wrap(err, "release: build changelog"))
	}

	preview := renderPreview(newTag, baseTag, changelog)
	fmt.Println(preview)

	if dryRun {
		slog.Info("dry-run — nothing pushed")
		return nil
	}
	if !yes && !promptConfirm("proceed?") {
		return errs.Usage(errs.New("release: aborted by user"))
	}

	tagBody := composeTagBody(msg, changelog)
	slog.Info("creating annotated tag", "tag", newTag)
	if _, err := gitRun(ctx, "tag", "-a", newTag, "-m", tagBody); err != nil {
		return errs.System(errs.Wrap(err, "release: git tag"))
	}
	slog.Info("pushing tag to origin", "tag", newTag)
	if _, err := gitRun(ctx, "push", "origin", newTag); err != nil {
		return errs.System(errs.Wrap(err, "release: git push"))
	}
	fmt.Printf("\nrelease complete — %s pushed\n", newTag)

	if !wait {
		return nil
	}
	return watchCI(ctx, newTag)
}

// ---------- guards ----------

func ensureReleasableBranch(ctx context.Context) error {
	out, err := gitRun(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return errs.Wrap(err, "release: detect current branch")
	}
	branch := strings.TrimSpace(out)
	if branch != "main" && branch != "master" {
		return errs.Errorf("release: current branch is %q; releases must be cut from main or master", branch)
	}
	slog.Debug("release: branch ok", "branch", branch)
	return nil
}

func ensurePristineTree(ctx context.Context) error {
	out, err := gitRun(ctx, "status", "--porcelain")
	if err != nil {
		return errs.Wrap(err, "release: git status")
	}
	if strings.TrimSpace(out) != "" {
		return errs.Errorf("release: working tree is not pristine — commit/stash first:\n%s", strings.TrimRight(out, "\n"))
	}
	return nil
}

// ---------- tag computation ----------

var calverTagRE = regexp.MustCompile(`^v(\d{8})\.(\d+)$`)

// computeNextTag returns the next v{YYYYMMDD}.N tag for `today`. N is
// one greater than the max N already present for today's date, or 1 if
// no tag for today exists. Tags for other days are ignored (we don't
// fill gaps across dates).
func computeNextTag(today time.Time, existing []string) string {
	datePart := today.UTC().Format("20060102")
	max := 0
	for _, t := range existing {
		m := calverTagRE.FindStringSubmatch(t)
		if m == nil || m[1] != datePart {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return fmt.Sprintf("v%s.%d", datePart, max+1)
}

func listAllTags(ctx context.Context) ([]string, error) {
	out, err := gitRun(ctx, "tag", "--list")
	if err != nil {
		return nil, err
	}
	var tags []string
	for line := range strings.SplitSeq(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

// describeLatestTag returns the most recent tag reachable from HEAD,
// or empty string on first release (no prior tags anywhere).
func describeLatestTag(ctx context.Context) (string, error) {
	out, err := gitRun(ctx, "describe", "--tags", "--abbrev=0")
	if err != nil {
		// git describe exits 128 when no tags exist; treat as first release.
		slog.Debug("release: no prior tag detected (first release)")
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// ---------- changelog ----------

const firstReleaseCap = 50

// buildChangelog produces the bullet-list body of the annotated tag
// message. For normal releases it's `git log baseTag..HEAD` subjects;
// for the very first release (no baseTag) it caps at firstReleaseCap
// commits and appends a "... and N more" line so we don't dump a novel
// into the tag body.
func buildChangelog(ctx context.Context, baseTag string) (string, error) {
	if baseTag == "" {
		out, err := gitRun(ctx, "log", "--pretty=format:- %s", "-n", strconv.Itoa(firstReleaseCap+1), "HEAD")
		if err != nil {
			return "", err
		}
		total, err := countCommits(ctx, "HEAD")
		if err != nil {
			return "", err
		}
		return formatChangelog(out, true, total), nil
	}
	out, err := gitRun(ctx, "log", "--pretty=format:- %s", baseTag+"..HEAD")
	if err != nil {
		return "", err
	}
	return formatChangelog(out, false, 0), nil
}

func countCommits(ctx context.Context, revspec string) (int, error) {
	out, err := gitRun(ctx, "rev-list", "--count", revspec)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// formatChangelog turns raw `git log` output into the final message
// body. First-release runs hit the cap logic; normal runs just
// trim and return. Empty log output becomes "(no changes)" so the tag
// body is never blank.
func formatChangelog(logOutput string, firstRelease bool, totalCommits int) string {
	trimmed := strings.TrimSpace(logOutput)
	if trimmed == "" {
		return "(no changes)"
	}
	lines := strings.Split(trimmed, "\n")
	if firstRelease && totalCommits > firstReleaseCap {
		lines = lines[:firstReleaseCap]
		lines = append(lines, fmt.Sprintf("... and %d more", totalCommits-firstReleaseCap))
	}
	return strings.Join(lines, "\n")
}

// renderPreview is the block shown to the user before they confirm.
// Kept as a pure string-returning function so it's easy to eyeball in
// tests without capturing stdout.
func renderPreview(newTag, baseTag, changelog string) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("release plan:\n")
	b.WriteString("  new tag:  " + newTag + "\n")
	if baseTag == "" {
		b.WriteString("  since:    (first release — no prior tag)\n")
	} else {
		b.WriteString("  since:    " + baseTag + "\n")
	}
	b.WriteString("\nchangelog:\n")
	for line := range strings.SplitSeq(changelog, "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// composeTagBody builds the annotated-tag message. An optional --message
// header sits above the changelog with a blank line between.
func composeTagBody(header, changelog string) string {
	if header == "" {
		return changelog
	}
	return header + "\n\n" + changelog
}

// ---------- CI watch ----------

// ghRun mirrors the subset of `gh run list --json ...` output we
// actually consume. Field names match gh's JSON keys.
type ghRun struct {
	DatabaseID   int64  `json:"databaseId"`
	WorkflowName string `json:"workflowName"`
	Status       string `json:"status"`     // queued, in_progress, completed
	Conclusion   string `json:"conclusion"` // success, failure, cancelled, timed_out, skipped, neutral
	URL          string `json:"url"`
}

// ghJob is the subset of `gh run view --json jobs` we use — enough to
// report which step of which job failed, with a link for the logs.
type ghJob struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Conclusion string   `json:"conclusion"`
	Steps      []ghStep `json:"steps"`
}

type ghStep struct {
	Name       string `json:"name"`
	Number     int    `json:"number"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type ghRunView struct {
	URL  string  `json:"url"`
	Jobs []ghJob `json:"jobs"`
}

// runState collapses gh's {status, conclusion} tuple into three buckets
// for the done-loop.
type runState int

const (
	runPending runState = iota
	runPassed
	runFailed
)

func classifyConclusion(status, conclusion string) runState {
	if status != "completed" {
		return runPending
	}
	switch conclusion {
	case "success", "skipped", "neutral":
		return runPassed
	default:
		return runFailed
	}
}

const (
	watchPollInterval = 3 * time.Second
	watchStartTimeout = 60 * time.Second
)

func watchCI(ctx context.Context, tag string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		slog.Warn("waiting for CI: skipped (install gh for --wait support)")
		return nil
	}
	sha, err := gitRun(ctx, "rev-list", "-n", "1", tag)
	if err != nil {
		return errs.System(errs.Wrap(err, "release: resolve tag sha"))
	}
	sha = strings.TrimSpace(sha)
	fmt.Printf("\nwaiting for CI (commit %s)...\n", shortSHA(sha))

	start := time.Now()
	printed := make(map[int64]string)
	for {
		runs, err := ghRunList(ctx, sha)
		if err != nil {
			return errs.System(errs.Wrap(err, "release: gh run list"))
		}
		elapsed := time.Since(start)
		for _, r := range runs {
			if line, changed := formatWatchStatusLine(elapsed, r, printed); changed {
				fmt.Println(line)
			}
		}
		if len(runs) > 0 && allTerminal(runs) {
			return summarizeCI(ctx, tag, runs, start)
		}
		if len(runs) == 0 && elapsed > watchStartTimeout {
			slog.Warn("no CI runs detected for this commit — tag is pushed but no workflow was triggered")
			return nil
		}

		select {
		case <-ctx.Done():
			fmt.Printf("\nrelease complete — %s pushed (CI watch cancelled)\n", tag)
			return nil
		case <-time.After(watchPollInterval):
		}
	}
}

// formatWatchStatusLine emits a status line only when the given run's
// (status, conclusion) tuple differs from what was last printed. Side
// effect: updates `prev` with the new state on a change so the next
// tick sees the current reality. Returns ("", false) when unchanged —
// callers skip printing.
func formatWatchStatusLine(elapsed time.Duration, r ghRun, prev map[int64]string) (string, bool) {
	key := r.Status + "/" + r.Conclusion
	if prev[r.DatabaseID] == key {
		return "", false
	}
	prev[r.DatabaseID] = key
	suffix := ""
	switch classifyConclusion(r.Status, r.Conclusion) {
	case runPassed:
		suffix = " ✓"
	case runFailed:
		suffix = " ✗"
	}
	status := r.Status
	if r.Status == "completed" && r.Conclusion != "" {
		status = r.Conclusion
	}
	return fmt.Sprintf("  [%s] %s: %s%s", fmtDuration(elapsed), r.WorkflowName, status, suffix), true
}

func allTerminal(runs []ghRun) bool {
	for _, r := range runs {
		if r.Status != "completed" {
			return false
		}
	}
	return true
}

// summarizeCI prints the final line after all workflows reach a
// terminal state. On success → print the shipped line. On failure →
// look up the first failed run's jobs + steps, print what broke and
// where, and return an errs.System so lazure exits with code 1.
func summarizeCI(ctx context.Context, tag string, runs []ghRun, start time.Time) error {
	dur := time.Since(start).Round(time.Second)
	for _, r := range runs {
		if classifyConclusion(r.Status, r.Conclusion) == runFailed {
			return reportFailedRun(ctx, tag, r)
		}
	}
	fmt.Printf("\nrelease complete — %s shipped (%s)\n", tag, dur)
	return nil
}

func reportFailedRun(ctx context.Context, tag string, r ghRun) error {
	fmt.Printf("\nrelease failed — %s tag pushed but CI did not succeed\n", tag)
	view, err := fetchGHRunView(ctx, r.DatabaseID)
	if err != nil {
		// Best-effort: we already know it failed; log the lookup error
		// but still exit 1 with what we have.
		slog.Debug("release: gh run view failed", "err", err)
		fmt.Printf("  %s (run %d) failed\n", r.WorkflowName, r.DatabaseID)
		if r.URL != "" {
			fmt.Printf("  %s\n", r.URL)
		}
		return errs.System(errs.Errorf("release: CI failed for %s", tag))
	}
	step, job := firstFailedStep(view.Jobs)
	switch {
	case step.Name != "":
		fmt.Printf("  %s (run %d) failed at step %q (job %q)\n", r.WorkflowName, r.DatabaseID, step.Name, job.Name)
	case job.Name != "":
		fmt.Printf("  %s (run %d) failed in job %q\n", r.WorkflowName, r.DatabaseID, job.Name)
	default:
		fmt.Printf("  %s (run %d) failed\n", r.WorkflowName, r.DatabaseID)
	}
	if view.URL != "" {
		fmt.Printf("  %s\n", view.URL)
	}
	fmt.Printf("\n  gh run view %d --log-failed    # to see logs\n", r.DatabaseID)
	return errs.System(errs.Errorf("release: CI failed for %s", tag))
}

// firstFailedStep finds the first failed step of the first failed job
// in a run view response. Both returns are zero values when nothing
// failed (shouldn't happen for a failed run, but defensive).
func firstFailedStep(jobs []ghJob) (ghStep, ghJob) {
	// Jobs come back in definition order; steps within a job in run order.
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	for _, j := range jobs {
		if j.Conclusion == "failure" || j.Conclusion == "cancelled" || j.Conclusion == "timed_out" {
			for _, s := range j.Steps {
				if s.Conclusion == "failure" || s.Conclusion == "cancelled" || s.Conclusion == "timed_out" {
					return s, j
				}
			}
			return ghStep{}, j
		}
	}
	return ghStep{}, ghJob{}
}

// ---------- subprocess wrappers ----------

func gitRun(ctx context.Context, args ...string) (string, error) {
	slog.Debug("release: git", "args", args)
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", errs.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", errs.Wrapf(err, "git %s", strings.Join(args, " "))
	}
	return string(out), nil
}

func ghRunList(ctx context.Context, sha string) ([]ghRun, error) {
	out, err := runGH(ctx, "run", "list",
		"--commit", sha,
		"--limit", "20",
		"--json", "databaseId,workflowName,status,conclusion,url",
	)
	if err != nil {
		return nil, err
	}
	return parseGHRunListJSON([]byte(out))
}

func fetchGHRunView(ctx context.Context, id int64) (ghRunView, error) {
	out, err := runGH(ctx, "run", "view", strconv.FormatInt(id, 10),
		"--json", "url,jobs",
	)
	if err != nil {
		return ghRunView{}, err
	}
	var v ghRunView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return ghRunView{}, errs.Wrap(err, "parse gh run view")
	}
	return v, nil
}

func runGH(ctx context.Context, args ...string) (string, error) {
	slog.Debug("release: gh", "args", args)
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", errs.Wrapf(err, "gh %s", strings.Join(args, " "))
	}
	return string(out), nil
}

func parseGHRunListJSON(b []byte) ([]ghRun, error) {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var runs []ghRun
	if err := json.Unmarshal(b, &runs); err != nil {
		return nil, errs.Wrap(err, "parse gh run list")
	}
	return runs, nil
}

// ---------- small helpers ----------

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
