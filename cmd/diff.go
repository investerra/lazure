package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/urfave/cli/v3"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
)

// DiffFlags are the diff-specific CLI flags for main.go to wire.
func DiffFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "format",
			Usage: "output format: unified|yaml|json",
			Value: "unified",
		},
		&cli.BoolFlag{
			Name:  "no-color",
			Usage: "disable ANSI color output (default: colors enabled when stdout is a TTY)",
		},
	}
}

// Diff implements `lazure diff <env>`. Compares the manifest's rendered
// ARM body against the currently-deployed app and prints the delta.
//
// Exit codes:
//
//	0  no drift
//	1  drift detected (errs.Drift)
//	2  system / auth / usage error
func Diff(ctx context.Context, c *cli.Command) error {
	env := c.StringArg("env")
	if env == "" {
		return errs.Usage(errs.New("diff: env argument is required"))
	}
	dir := c.String("dir")
	format := c.String("format")
	noColor := c.Bool("no-color")
	slog.Debug("diff: start", "env", env, "dir", dir, "format", format, "no_color", noColor)

	manifest, vars, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{
		ProjectDir: dir, Env: env,
	})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "diff: load manifest"))
	}
	if r := lazurecfg.Validate(manifest); r.HasErrors() {
		return errs.Validation(errs.Wrap(r.Err(), "diff"))
	}

	sub := manifest.App.Identity.SubscriptionID()
	if sub == "" {
		return errs.Usage(errs.Errorf("diff: could not derive subscription id from app.identity %q", manifest.App.Identity))
	}
	rg, name := manifest.App.ResourceGroup, manifest.App.Name

	slog.Debug("diff: creating Azure credential")
	tokens, err := azureapi.NewTokenProvider()
	if err != nil {
		return errs.Auth(errs.Wrap(err, "diff: auth"))
	}
	ca := azureapi.NewContainerAppsClient(tokens)

	slog.Debug("diff: fetching deployed app state")
	actual, err := ca.Get(ctx, sub, rg, name)
	switch {
	case errors.Is(err, azureapi.ErrContainerAppNotFound):
		slog.Info("app not deployed yet — diff treats deployed-side as empty", "app", name)
		actual = &azurearm.ContainerApp{}
	case err != nil:
		return errs.System(errs.Wrap(err, "diff: fetch deployed state"))
	}

	// Build expected from the manifest. If actual is a real deployed
	// revision, use its latest revision name for traffic.previous
	// resolution (same as deploy would do).
	vaultURL, _ := vars["keyvault_url"].(string)
	expected, err := azurearm.Transform(manifest, azurearm.TransformOptions{
		VaultURL:         vaultURL,
		PreviousRevision: actual.Properties.LatestRevisionName,
	})
	if err != nil {
		return errs.System(errs.Wrap(err, "diff: transform"))
	}

	// Normalize both sides: zero read-only, sort unordered arrays.
	azureapi.Normalize(expected)
	azureapi.Normalize(actual)

	expectedBytes, err := yaml.Marshal(expected)
	if err != nil {
		return errs.System(errs.Wrap(err, "diff: marshal expected"))
	}
	actualBytes, err := yaml.Marshal(actual)
	if err != nil {
		return errs.System(errs.Wrap(err, "diff: marshal actual"))
	}

	if string(expectedBytes) == string(actualBytes) {
		slog.Info("no drift", "env", env, "app", name)
		return nil
	}

	if err := writeDiff(format, actualBytes, expectedBytes, !shouldColor(noColor)); err != nil {
		return err
	}

	// Drift detected → exit 1 via errs.Drift.
	return errs.Drift(errs.Errorf("drift detected between deployed and rendered %s", name))
}

// shouldColor returns true when the unified diff output should be
// colorized. User opt-out via --no-color wins. Otherwise auto-detect:
// color if stdout is a TTY AND the NO_COLOR environment variable is
// unset (https://no-color.org).
func shouldColor(noColorFlag bool) bool {
	if noColorFlag {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// writeDiff emits the comparison in the requested format. unified is
// the classic `diff -u` output; yaml and json dump both sides raw for
// tooling / eyeballing. plain disables ANSI colors regardless of
// terminal type.
func writeDiff(format string, actual, expected []byte, plain bool) error {
	switch format {
	case "", "unified":
		d := difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(actual)),
			B:        difflib.SplitLines(string(expected)),
			FromFile: "deployed",
			ToFile:   "rendered",
			Context:  3,
		}
		out, err := difflib.GetUnifiedDiffString(d)
		if err != nil {
			return errs.System(errs.Wrap(err, "diff: compute unified"))
		}
		if !plain {
			out = colorizeUnifiedDiff(out)
		}
		fmt.Print(out)
		return nil

	case "yaml":
		fmt.Println("--- deployed.yaml ---")
		fmt.Print(string(actual))
		fmt.Println("--- rendered.yaml ---")
		fmt.Print(string(expected))
		return nil

	case "json":
		deployedJSON, err := yaml.YAMLToJSON(actual)
		if err != nil {
			return errs.System(errs.Wrap(err, "diff: yaml->json (deployed)"))
		}
		renderedJSON, err := yaml.YAMLToJSON(expected)
		if err != nil {
			return errs.System(errs.Wrap(err, "diff: yaml->json (rendered)"))
		}
		fmt.Println(`{"deployed":`)
		fmt.Println(string(deployedJSON))
		fmt.Println(`,"rendered":`)
		fmt.Println(string(renderedJSON))
		fmt.Println("}")
		return nil

	default:
		return errs.Usage(errs.Errorf("diff: invalid --format %q (want unified|yaml|json)", format))
	}
}

// Pre-computed styles for colorizeUnifiedDiff. Soft/muted palette that
// reads well on both dark and light terminals without screaming.
var (
	styleAdd  = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // soft sage green
	styleDel  = lipgloss.NewStyle().Foreground(lipgloss.Color("174")) // soft coral
	styleHdr  = lipgloss.NewStyle().Foreground(lipgloss.Color("110")) // soft blue
	styleHunk = lipgloss.NewStyle().Foreground(lipgloss.Color("139")) // muted violet
)

// colorizeUnifiedDiff walks a unified-diff string line by line and
// wraps each line in an ANSI style based on its leading character:
//
//	"--- ", "+++ " → file headers (bold cyan)
//	"@@ "          → hunk markers (purple)
//	"+"            → addition    (green)
//	"-"            → removal     (red)
//	anything else  → unchanged context (no style)
//
// Operates on the already-generated diff text rather than at emit time
// so we don't have to fork difflib or write our own unified formatter.
func colorizeUnifiedDiff(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	var sb strings.Builder
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		switch {
		case strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- "):
			sb.WriteString(styleHdr.Render(line))
		case strings.HasPrefix(line, "@@ "):
			sb.WriteString(styleHunk.Render(line))
		case strings.HasPrefix(line, "+"):
			sb.WriteString(styleAdd.Render(line))
		case strings.HasPrefix(line, "-"):
			sb.WriteString(styleDel.Render(line))
		default:
			sb.WriteString(line)
		}
	}
	return sb.String()
}
