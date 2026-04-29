package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
	"github.com/investerra/lazure/internal/sopsio"
)

// DoctorFlags are the flags for `lazure doctor`.
func DoctorFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "format", Value: "text", Usage: "output format: text|json"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors (also honored via NO_COLOR env)"},
	}
}

// Doctor implements `lazure doctor`. Preflight diagnostic that runs a
// sequence of checks and reports per-check pass/fail. Exit 0 when all
// checks pass (warnings don't fail); exit 1 on any failure.
//
// Checks are grouped into two sections — global (run unconditionally)
// and project (only when a deploy.yml is present in --dir). Per-env
// subchecks short-circuit: if the vars file is missing, the following
// subchecks for that env are rendered as "—" rather than attempted.
// This keeps the output focused on the first real problem per env.
func Doctor(ctx context.Context, c *cli.Command) error {
	format := c.String("format")
	color := shouldColor(c.Bool("no-color"))
	dir := c.String("dir")
	slog.Debug("doctor: start", "dir", dir, "format", format, "color", color)
	progress := newDoctorProgress()
	defer progress.Stop()

	sections := []section{
		{name: "global checks", results: runGlobalChecks(ctx, progress)},
	}
	if proj := runProjectChecksWithProgress(ctx, dir, progress); proj != nil {
		sections = append(sections, *proj)
	}
	progress.Stop()

	switch format {
	case "", "text":
		fmt.Println(renderDoctorText(sections, color))
	case "json":
		out, err := renderDoctorJSON(sections)
		if err != nil {
			return errs.System(errs.Wrap(err, "doctor: json"))
		}
		fmt.Println(out)
	default:
		return errs.Usage(errs.Errorf("doctor: unknown --format %q (want text|json)", format))
	}

	if anyFailed(sections) {
		return errs.System(errs.New("doctor: one or more checks failed"))
	}
	return nil
}

// ---------- data model ----------

type checkStatus int

const (
	statusPass checkStatus = iota
	statusWarn
	statusFail
	statusSkip
)

func (s checkStatus) String() string {
	switch s {
	case statusPass:
		return "pass"
	case statusWarn:
		return "warn"
	case statusFail:
		return "fail"
	case statusSkip:
		return "skip"
	default:
		return "?"
	}
}

type checkResult struct {
	status checkStatus
	name   string
	detail string
}

type envCheck struct {
	env     string
	status  checkStatus // worst of the subchecks; determines the env line's mark
	overall string      // e.g. "vars ✓  secrets decrypt ✓  manifest renders ✓  KV reachable ✓"
	stages  map[string]checkStatus
	failMsg string // populated only on failure — the underlying error text
}

type section struct {
	name      string
	results   []checkResult
	envChecks []envCheck // populated only for the project section
}

// ---------- global checks ----------

type doctorProgress struct {
	sp *waitSpinner
}

func newDoctorProgress() *doctorProgress {
	sp := newWaitSpinner(time.Time{})
	sp.SetMessage("doctor: starting checks")
	sp.Start()
	return &doctorProgress{sp: sp}
}

func (p *doctorProgress) Set(msg string) {
	if p == nil || p.sp == nil {
		return
	}
	p.sp.SetMessage(msg)
}

func (p *doctorProgress) Stop() {
	if p == nil || p.sp == nil {
		return
	}
	p.sp.Stop()
}

func runGlobalChecks(ctx context.Context, progress *doctorProgress) []checkResult {
	checks := []struct {
		label string
		fn    func() checkResult
	}{
		{label: "checking git", fn: checkGit},
		{label: "checking editor", fn: checkEditor},
		{label: "checking az", fn: checkAz},
		{label: "checking gh", fn: checkGH},
		{label: "checking Azure auth", fn: func() checkResult { return checkAzureAuth(ctx) }},
	}
	results := make([]checkResult, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		i, check := i, check
		wg.Add(1)
		go func() {
			defer wg.Done()
			progress.Set(check.label)
			results[i] = check.fn()
		}()
	}
	wg.Wait()
	return results
}

func checkGit() checkResult {
	path, err := exec.LookPath("git")
	if err != nil {
		return checkResult{status: statusFail, name: "git", detail: "not found on PATH"}
	}
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return checkResult{status: statusFail, name: "git", detail: path + " present but `git --version` failed: " + err.Error()}
	}
	return checkResult{status: statusPass, name: "git", detail: parseGitVersion(string(out))}
}

// parseGitVersion pulls "2.43.0" out of "git version 2.43.0\n". If the
// shape is unexpected, the whole line is returned verbatim.
func parseGitVersion(s string) string {
	s = strings.TrimSpace(s)
	const prefix = "git version "
	if strings.HasPrefix(s, prefix) {
		return strings.TrimPrefix(s, prefix)
	}
	return s
}

func checkEditor() checkResult {
	if v := os.Getenv("EDITOR"); v != "" {
		return checkResult{status: statusPass, name: "editor", detail: "$EDITOR=" + v}
	}
	if v := os.Getenv("VISUAL"); v != "" {
		return checkResult{status: statusPass, name: "editor", detail: "$VISUAL=" + v}
	}
	return checkResult{
		status: statusFail,
		name:   "editor",
		detail: "neither $EDITOR nor $VISUAL is set — needed by `secrets edit` / `vars edit`",
	}
}

func checkAz() checkResult {
	if _, err := exec.LookPath("az"); err != nil {
		return checkResult{
			status: statusWarn,
			name:   "az",
			detail: "not installed — `lazure exec` will not work",
		}
	}
	out, err := exec.Command("az", "--version").Output()
	if err != nil {
		return checkResult{status: statusWarn, name: "az", detail: "present but `az --version` failed: " + err.Error()}
	}
	return checkResult{
		status: statusPass,
		name:   "az",
		detail: compactFirstLine(string(out)) + " (optional; required only for `lazure exec`)",
	}
}

// checkGH is WARN rather than FAIL because gh is only needed by
// `lazure release --wait` for post-push CI monitoring. Release itself
// (tag + push) doesn't touch gh, and `--wait` without gh just skips
// watching gracefully — the user still got their tag pushed.
func checkGH() checkResult {
	if _, err := exec.LookPath("gh"); err != nil {
		return checkResult{
			status: statusWarn,
			name:   "gh",
			detail: "not installed — `lazure release --wait` won't monitor CI (release still works)",
		}
	}
	out, err := exec.Command("gh", "--version").Output()
	if err != nil {
		return checkResult{status: statusWarn, name: "gh", detail: "present but `gh --version` failed: " + err.Error()}
	}
	return checkResult{
		status: statusPass,
		name:   "gh",
		detail: compactFirstLine(string(out)) + " (optional; required only for `lazure release --wait`)",
	}
}

// compactFirstLine returns the first non-empty line with internal
// whitespace collapsed to single spaces. `az --version` output is
// column-aligned with many spaces between "azure-cli" and the version
// number; raw output would read poorly in the doctor display.
func compactFirstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return strings.Join(strings.Fields(t), " ")
		}
	}
	return ""
}

func checkAzureAuth(ctx context.Context) checkResult {
	tp, err := azureapi.NewTokenProvider()
	if err != nil {
		return checkResult{status: statusFail, name: "azure auth", detail: "NewTokenProvider: " + err.Error()}
	}
	if _, err := tp.Management(ctx); err != nil {
		return checkResult{
			status: statusFail,
			name:   "azure auth",
			detail: "failed to acquire management.azure.com token — try `az login`:\n" + err.Error(),
		}
	}
	return checkResult{
		status: statusPass,
		name:   "azure auth",
		detail: "DefaultAzureCredential to management.azure.com",
	}
}

// ---------- project checks ----------

// runProjectChecks scans --dir for a deploy.yml (fallback lazure.yml).
// Absent → returns nil, caller skips the section entirely. Present →
// returns a populated section with a manifest-found result and one
// envCheck per discovered env.
func runProjectChecks(ctx context.Context, dir string) *section {
	return runProjectChecksWithProgress(ctx, dir, nil)
}

func runProjectChecksWithProgress(ctx context.Context, dir string, progress *doctorProgress) *section {
	progress.Set("checking project files")
	manifestPath := findManifest(dir)
	envs := discoverEnvs(dir)
	if manifestPath == "" && len(envs) == 0 {
		slog.Debug("doctor: no manifest in dir, skipping project section", "dir", dir)
		return nil
	}
	sec := &section{
		name:    "project checks",
		results: projectPathChecks(dir, manifestPath, envs),
	}
	if manifestPath == "" {
		return sec
	}
	if len(envs) == 0 {
		return sec
	}
	sec.envChecks = make([]envCheck, len(envs))
	var wg sync.WaitGroup
	for i, env := range envs {
		i, env := i, env
		wg.Add(1)
		go func() {
			defer wg.Done()
			sec.envChecks[i] = checkEnvWithProgress(ctx, dir, env, progress)
		}()
	}
	wg.Wait()
	return sec
}

func projectPathChecks(dir, manifestPath string, envs []string) []checkResult {
	envsDir := filepath.Join(dir, "envs")
	manifest := checkResult{status: statusPass, name: "manifest", detail: manifestPath}
	if manifestPath == "" {
		manifest = checkResult{status: statusFail, name: "manifest", detail: "not found at " + filepath.Join(dir, "deploy.yml")}
	}
	envList := checkResult{status: statusPass, name: "envs", detail: strings.Join(envs, ", ")}
	if len(envs) == 0 {
		envList = checkResult{status: statusWarn, name: "envs", detail: "no envs/*.vars.yml found"}
	}
	return []checkResult{
		{status: statusPass, name: "project dir", detail: filepath.Clean(dir)},
		manifest,
		pathCheck("envs dir", envsDir, statusWarn),
		envList,
		pathCheck("sops config", sopsConfigPath(dir), statusWarn),
		pathCheck("schema", filepath.Join(dir, "deploy.schema.json"), statusWarn),
	}
}

func pathCheck(name, path string, missingStatus checkStatus) checkResult {
	if _, err := os.Stat(path); err == nil {
		return checkResult{status: statusPass, name: name, detail: path}
	}
	return checkResult{status: missingStatus, name: name, detail: "not found at " + path}
}

// findManifest returns the path to deploy.yml if present, falling back
// to lazure.yml for the legacy naming. Empty return = no manifest.
func findManifest(dir string) string {
	for _, name := range []string{"deploy.yml", "lazure.yml"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// discoverEnvs returns env names implied by envs/{name}.vars.yml files.
// Files ending in `.plain.yml` or `.secrets.yml` are filtered out; so is
// anything without the .vars.yml suffix. Results are sorted for stable
// output between runs.
func discoverEnvs(dir string) []string {
	pattern := filepath.Join(dir, "envs", "*.vars.yml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	var envs []string
	for _, m := range matches {
		base := filepath.Base(m)
		if strings.HasSuffix(base, ".plain.yml") || strings.HasSuffix(base, ".secrets.yml") {
			continue
		}
		name := strings.TrimSuffix(base, ".vars.yml")
		envs = append(envs, name)
	}
	sort.Strings(envs)
	return envs
}

// checkEnvWithProgress runs the per-env subchecks in short-circuiting
// order: vars file exists → secrets file exists → sops decrypts →
// manifest renders → subscription reachable → KV reachable. First
// failure aborts the remaining subchecks, which are rendered as "—".
// This mirrors how a user would debug by hand: fix the earliest
// problem, then re-run.
func checkEnvWithProgress(ctx context.Context, dir, env string, progress *doctorProgress) envCheck {
	ec := envCheck{env: env, status: statusPass}

	marks := map[string]string{
		"vars":             "skip",
		"secrets decrypt":  "skip",
		"manifest renders": "skip",
		"sub reachable":    "skip",
		"KV reachable":     "skip",
	}
	order := []string{"vars", "secrets decrypt", "manifest renders", "sub reachable", "KV reachable"}

	fail := func(stage, msg string) envCheck {
		ec.status = statusFail
		ec.failMsg = stage + ": " + msg
		ec.overall = joinMarks(order, marks)
		ec.stages = envStages(marks)
		return ec
	}

	varsPath := filepath.Join(dir, "envs", env+".vars.yml")
	progress.Set("checking " + env + " vars")
	if _, err := os.Stat(varsPath); err != nil {
		marks["vars"] = "fail"
		return fail("vars file", err.Error())
	}
	marks["vars"] = "pass"

	secretsPath := filepath.Join(dir, "envs", env+".secrets.yml")
	progress.Set("checking " + env + " secrets")
	if _, err := os.Stat(secretsPath); err != nil {
		marks["secrets decrypt"] = "fail"
		return fail("secrets file", err.Error())
	}

	progress.Set("decrypting " + env + " secrets")
	if _, err := sopsio.Decrypt(secretsPath); err != nil {
		marks["secrets decrypt"] = "fail"
		return fail("sops decrypt", err.Error())
	}
	marks["secrets decrypt"] = "pass"

	progress.Set("rendering " + env + " manifest")
	manifest, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: dir, Env: env})
	if err != nil {
		marks["manifest renders"] = "fail"
		return fail("manifest render", err.Error())
	}
	marks["manifest renders"] = "pass"

	// Subscription probe — catches tenant mismatches up front (a
	// common footgun with multi-sub setups: az logged into dev but
	// the user runs lazure deploy prd).
	tp, err := azureapi.NewTokenProvider()
	if err != nil {
		marks["sub reachable"] = "fail"
		return fail("azure auth", err.Error())
	}
	subID := manifest.App.Identity.SubscriptionID()
	if subID == "" {
		marks["sub reachable"] = "fail"
		return fail("sub id", fmt.Sprintf("could not derive subscription id from app.identity %q", manifest.App.Identity))
	}
	progress.Set("checking " + env + " subscription")
	if _, err := azureapi.LookupSubscription(ctx, tp, subID); err != nil {
		marks["sub reachable"] = "fail"
		return fail("sub probe", subProbeHint(err, subID))
	}
	marks["sub reachable"] = "pass"

	progress.Set("checking " + env + " key vault")
	vaultURL, err := sopsio.VaultURL(secretsPath)
	if err != nil {
		marks["KV reachable"] = "fail"
		return fail("sops vault url", err.Error())
	}
	kv := azureapi.NewKeyVaultClient(vaultURL, tp)
	if _, err := kv.ListSecrets(ctx); err != nil {
		marks["KV reachable"] = "fail"
		return fail("kv list", err.Error())
	}
	marks["KV reachable"] = "pass"

	ec.overall = joinMarks(order, marks)
	ec.stages = envStages(marks)
	return ec
}

func envStages(marks map[string]string) map[string]checkStatus {
	out := make(map[string]checkStatus, len(marks))
	for k, v := range marks {
		switch v {
		case "pass":
			out[k] = statusPass
		case "fail":
			out[k] = statusFail
		default:
			out[k] = statusSkip
		}
	}
	return out
}

// subProbeHint returns a tighter, more actionable message for the
// usual subscription-probe failures than the wrapped error text would
// give on its own.
func subProbeHint(err error, subID string) string {
	switch {
	case errors.Is(err, azureapi.ErrSubscriptionAuth):
		return fmt.Sprintf("token rejected for %s — wrong tenant? try `az login --tenant <id>`", subID)
	case errors.Is(err, azureapi.ErrSubscriptionForbidden):
		return fmt.Sprintf("forbidden on %s — your account lacks Reader RBAC", subID)
	case errors.Is(err, azureapi.ErrSubscriptionNotFound):
		return fmt.Sprintf("subscription %s not found — typo in app.identity?", subID)
	default:
		return err.Error()
	}
}

func joinMarks(order []string, marks map[string]string) string {
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s %s", k, marks[k]))
	}
	return strings.Join(parts, "  ")
}

// ---------- aggregation ----------

func anyFailed(sections []section) bool {
	for _, sec := range sections {
		for _, r := range sec.results {
			if r.status == statusFail {
				return true
			}
		}
		for _, ec := range sec.envChecks {
			if ec.status == statusFail {
				return true
			}
		}
	}
	return false
}

// ---------- rendering ----------

func renderDoctorText(sections []section, color bool) string {
	var b strings.Builder
	b.WriteString("\nlazure doctor\n")

	for _, sec := range sections {
		b.WriteString("\n")
		b.WriteString(sectionTitle(sec.name))
		b.WriteString("\n")
		renderCheckList(&b, sec.results, color)
		if len(sec.envChecks) > 0 {
			b.WriteString("\n")
			b.WriteString("Environments\n")
			renderEnvBlocks(&b, sec.envChecks, color)
		}
	}

	findings := doctorFindings(sections)
	if len(findings) > 0 {
		b.WriteString("\nFindings\n")
		for _, f := range findings {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}

	b.WriteString("\n")
	if anyFailed(sections) {
		b.WriteString(colorize("one or more checks failed", styleFail, color))
	} else {
		b.WriteString(colorize("all checks passed", stylePass, color))
	}
	b.WriteString("\n")
	return b.String()
}

func sectionTitle(name string) string {
	switch name {
	case "global checks":
		return "Global Checks"
	case "project checks":
		return "Project Checks"
	default:
		return name
	}
}

func renderCheckList(b *strings.Builder, results []checkResult, color bool) {
	for _, r := range results {
		fmt.Fprintf(b, "  %-4s  %-12s  %s\n",
			statusWord(r.status, color),
			r.name,
			colorize(r.detail, styleForStatus(r.status), color))
	}
}

func renderEnvBlocks(b *strings.Builder, checks []envCheck, color bool) {
	for _, ec := range checks {
		fmt.Fprintf(b, "  %s\n", ec.env)
		fmt.Fprintf(b, "    %-12s  %s\n", "vars", stageWord(ec, "vars", color))
		fmt.Fprintf(b, "    %-12s  %s\n", "secrets", stageWord(ec, "secrets decrypt", color))
		fmt.Fprintf(b, "    %-12s  %s\n", "manifest", stageWord(ec, "manifest renders", color))
		fmt.Fprintf(b, "    %-12s  %s\n", "subscription", stageWord(ec, "sub reachable", color))
		fmt.Fprintf(b, "    %-12s  %s\n", "key vault", stageWord(ec, "KV reachable", color))
	}
}

func stageWord(ec envCheck, stage string, color bool) string {
	if ec.stages == nil {
		return statusWord(statusSkip, color)
	}
	return statusWord(ec.stages[stage], color)
}

func doctorFindings(sections []section) []string {
	var out []string
	for _, sec := range sections {
		name := sectionTitle(sec.name)
		for _, r := range sec.results {
			if r.status != statusPass {
				out = append(out, fmt.Sprintf("%s / %s: %s", name, r.name, r.detail))
			}
		}
		for _, ec := range sec.envChecks {
			if ec.status != statusPass && ec.failMsg != "" {
				out = append(out, fmt.Sprintf("env %s: %s", ec.env, compactFinding(ec.failMsg)))
			}
		}
	}
	return out
}

func compactFinding(s string) string {
	lines := strings.Fields(strings.TrimSpace(s))
	return strings.Join(lines, " ")
}

func statusWord(s checkStatus, color bool) string {
	switch s {
	case statusPass:
		return colorize("OK", stylePass, color)
	case statusWarn:
		return colorize("WARN", styleWarn, color)
	case statusFail:
		return colorize("FAIL", styleFail, color)
	case statusSkip:
		return colorize("SKIP", styleSkip, color)
	default:
		return "?"
	}
}

func styleForStatus(s checkStatus) lipgloss.Style {
	switch s {
	case statusFail:
		return styleFail
	case statusWarn:
		return styleWarn
	case statusSkip:
		return styleSkip
	default:
		return lipgloss.NewStyle()
	}
}

var (
	stylePass = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // sage green
	styleWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("179")) // muted amber
	styleFail = lipgloss.NewStyle().Foreground(lipgloss.Color("174")) // soft coral
	styleSkip = lipgloss.NewStyle().Foreground(lipgloss.Color("241")) // dim gray
)

// ---------- JSON rendering ----------

type doctorJSONCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type doctorJSONEnv struct {
	Env     string `json:"env"`
	Status  string `json:"status"`
	Overall string `json:"overall"`
	FailMsg string `json:"fail_msg,omitempty"`
}

type doctorJSONSection struct {
	Name   string            `json:"name"`
	Checks []doctorJSONCheck `json:"checks,omitempty"`
	Envs   []doctorJSONEnv   `json:"envs,omitempty"`
}

type doctorJSONOutput struct {
	Sections    []doctorJSONSection `json:"sections"`
	Passed      bool                `json:"passed"`
	FailedCount int                 `json:"failed_count"`
}

func renderDoctorJSON(sections []section) (string, error) {
	out := doctorJSONOutput{Passed: !anyFailed(sections)}
	for _, sec := range sections {
		js := doctorJSONSection{Name: sec.name}
		for _, r := range sec.results {
			js.Checks = append(js.Checks, doctorJSONCheck{
				Name: r.name, Status: r.status.String(), Detail: r.detail,
			})
			if r.status == statusFail {
				out.FailedCount++
			}
		}
		for _, ec := range sec.envChecks {
			je := doctorJSONEnv{
				Env: ec.env, Status: ec.status.String(),
				Overall: ec.overall, FailMsg: ec.failMsg,
			}
			js.Envs = append(js.Envs, je)
			if ec.status == statusFail {
				out.FailedCount++
			}
		}
		out.Sections = append(out.Sections, js)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
