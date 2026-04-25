package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

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

	sections := []section{
		{name: "global checks", results: runGlobalChecks(ctx)},
	}
	if proj := runProjectChecks(ctx, dir); proj != nil {
		sections = append(sections, *proj)
	}

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
	failMsg string      // populated only on failure — the underlying error text
}

type section struct {
	name      string
	results   []checkResult
	envChecks []envCheck // populated only for the project section
}

// ---------- global checks ----------

func runGlobalChecks(ctx context.Context) []checkResult {
	return []checkResult{
		checkGit(),
		checkEditor(),
		checkAz(),
		checkGH(),
		checkAzureAuth(ctx),
	}
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
		detail: compactFirstLine(string(out)) + " (optional — required only for `lazure exec`)",
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
		detail: compactFirstLine(string(out)) + " (optional — required only for `lazure release --wait`)",
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
		detail: "DefaultAzureCredential → management.azure.com",
	}
}

// ---------- project checks ----------

// runProjectChecks scans --dir for a deploy.yml (fallback lazure.yml).
// Absent → returns nil, caller skips the section entirely. Present →
// returns a populated section with a manifest-found result and one
// envCheck per discovered env.
func runProjectChecks(ctx context.Context, dir string) *section {
	manifestPath := findManifest(dir)
	if manifestPath == "" {
		slog.Debug("doctor: no manifest in dir, skipping project section", "dir", dir)
		return nil
	}
	sec := &section{
		name: "project checks",
		results: []checkResult{
			{status: statusPass, name: "manifest", detail: "found at " + manifestPath},
		},
	}
	envs := discoverEnvs(dir)
	if len(envs) == 0 {
		sec.results = append(sec.results, checkResult{
			status: statusWarn,
			name:   "envs",
			detail: "no envs/*.vars.yml found",
		})
		return sec
	}
	for _, env := range envs {
		sec.envChecks = append(sec.envChecks, checkEnv(ctx, dir, env))
	}
	return sec
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

// checkEnv runs the per-env subchecks in short-circuiting order:
// vars file exists → secrets file exists → sops decrypts → manifest
// renders → KV reachable. First failure aborts the remaining subchecks,
// which are rendered as "—". This mirrors how a user would debug by
// hand: fix the earliest problem, then re-run.
func checkEnv(ctx context.Context, dir, env string) envCheck {
	ec := envCheck{env: env, status: statusPass}

	marks := map[string]string{
		"vars":             "—",
		"secrets decrypt":  "—",
		"manifest renders": "—",
		"KV reachable":     "—",
	}
	order := []string{"vars", "secrets decrypt", "manifest renders", "KV reachable"}

	fail := func(stage, msg string) envCheck {
		ec.status = statusFail
		ec.failMsg = stage + ": " + msg
		ec.overall = joinMarks(order, marks)
		return ec
	}

	varsPath := filepath.Join(dir, "envs", env+".vars.yml")
	if _, err := os.Stat(varsPath); err != nil {
		marks["vars"] = "✗"
		return fail("vars file", err.Error())
	}
	marks["vars"] = "✓"

	secretsPath := filepath.Join(dir, "envs", env+".secrets.yml")
	if _, err := os.Stat(secretsPath); err != nil {
		marks["secrets decrypt"] = "✗"
		return fail("secrets file", err.Error())
	}

	if _, err := sopsio.Decrypt(secretsPath); err != nil {
		marks["secrets decrypt"] = "✗"
		return fail("sops decrypt", err.Error())
	}
	marks["secrets decrypt"] = "✓"

	if _, _, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{ProjectDir: dir, Env: env}); err != nil {
		marks["manifest renders"] = "✗"
		return fail("manifest render", err.Error())
	}
	marks["manifest renders"] = "✓"

	vaultURL, err := sopsio.VaultURL(secretsPath)
	if err != nil {
		marks["KV reachable"] = "✗"
		return fail("sops vault url", err.Error())
	}
	tp, err := azureapi.NewTokenProvider()
	if err != nil {
		marks["KV reachable"] = "✗"
		return fail("kv auth", err.Error())
	}
	kv := azureapi.NewKeyVaultClient(vaultURL, tp)
	if _, err := kv.ListSecrets(ctx); err != nil {
		marks["KV reachable"] = "✗"
		return fail("kv list", err.Error())
	}
	marks["KV reachable"] = "✓"

	ec.overall = joinMarks(order, marks)
	return ec
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
		b.WriteString(sec.name)
		b.WriteString("\n")
		for _, r := range sec.results {
			b.WriteString("  ")
			b.WriteString(formatCheckLine(r, color))
			b.WriteString("\n")
		}
		if len(sec.envChecks) > 0 {
			b.WriteString("  environments:\n")
			for _, ec := range sec.envChecks {
				b.WriteString("    ")
				mark := markFor(ec.status, color)
				fmt.Fprintf(&b, "%s %s    %s\n", mark, padRight(ec.env, 8), ec.overall)
				if ec.status == statusFail && ec.failMsg != "" {
					for i, line := range strings.Split(strings.TrimSpace(ec.failMsg), "\n") {
						prefix := "        └─ "
						if i > 0 {
							prefix = "           "
						}
						b.WriteString(colorize(prefix+line, styleFail, color))
						b.WriteString("\n")
					}
				}
			}
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

func formatCheckLine(r checkResult, color bool) string {
	mark := markFor(r.status, color)
	name := padRight(r.name, 16)
	detail := r.detail
	if r.status == statusFail && strings.Contains(detail, "\n") {
		// Indent continuation lines so multi-line error text stays under
		// the detail column rather than disrupting the next check.
		lines := strings.Split(detail, "\n")
		for i := 1; i < len(lines); i++ {
			lines[i] = strings.Repeat(" ", 2+1+16) + lines[i]
		}
		detail = strings.Join(lines, "\n")
	}
	return fmt.Sprintf("%s %s %s", mark, name, colorize(detail, styleForStatus(r.status), color))
}

func markFor(s checkStatus, color bool) string {
	switch s {
	case statusPass:
		return colorize("[✓]", stylePass, color)
	case statusWarn:
		return colorize("[!]", styleWarn, color)
	case statusFail:
		return colorize("[✗]", styleFail, color)
	case statusSkip:
		return colorize("[-]", styleSkip, color)
	default:
		return "[?]"
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
