package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

func WaitForDeployFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "expected-sha", Usage: "expected commit SHA (default: EXPECTED_SHA, then current git HEAD)"},
		&cli.StringFlag{Name: "path", Usage: "version endpoint path", Value: "/version"},
		&cli.StringFlag{Name: "field", Usage: "JSON field to compare", Value: "commit"},
		&cli.DurationFlag{Name: "timeout", Usage: "max wait time", Value: 5 * time.Minute},
		&cli.DurationFlag{Name: "interval", Usage: "delay between checks", Value: 10 * time.Second},
	}
}

// WaitForDeploy implements `lazure wait-for-deploy <env>`. It resolves
// the externally-routed app URL from Container Apps ingress, then polls
// the app's version endpoint until the reported commit matches the
// expected SHA.
func WaitForDeploy(ctx context.Context, c *cli.Command) error {
	expected := firstNonEmpty(c.String("expected-sha"), os.Getenv("EXPECTED_SHA"), currentGitSHA(ctx))
	if expected == "" {
		return errs.Usage(errs.New("wait-for-deploy: expected SHA is required (pass --expected-sha, set EXPECTED_SHA, or run in a git repo)"))
	}
	t, err := loadAzureTarget(c, "wait-for-deploy")
	if err != nil {
		return err
	}
	slog.Debug("wait-for-deploy: fetching app", "app", t.Name, "env", t.Env)
	app, err := t.CA.Get(ctx, t.Sub, t.RG, t.Name)
	switch {
	case err == nil:
	case errors.Is(err, azureapi.ErrContainerAppNotFound):
		return errs.Usage(errs.Errorf("wait-for-deploy: app %q not deployed yet", t.Name))
	default:
		return errs.System(errs.Wrap(err, "wait-for-deploy: get app"))
	}

	u, err := deploymentVersionURL(app, c.String("path"))
	if err != nil {
		return errs.Usage(errs.Wrap(err, "wait-for-deploy"))
	}
	fmt.Fprintf(os.Stdout, "Expected version: %s\n", expected)
	fmt.Fprintf(os.Stdout, "Polling: %s\n", u)
	if err := waitForDeploymentVersion(ctx, u, expected, c.String("field"), c.Duration("timeout"), c.Duration("interval"), os.Stdout); err != nil {
		return errs.WithCode(errs.CodeTask, err)
	}
	fmt.Fprintln(os.Stdout, "deployment ready")
	return nil
}

func deploymentVersionURL(app *azurearm.ContainerApp, path string) (string, error) {
	if app == nil || app.Properties.Configuration.Ingress == nil {
		return "", errs.New("no ingress configured")
	}
	ing := app.Properties.Configuration.Ingress
	host := ""
	for _, d := range ing.CustomDomains {
		if strings.TrimSpace(d.Name) != "" {
			host = strings.TrimSpace(d.Name)
			break
		}
	}
	if host == "" {
		host = strings.TrimSpace(ing.FQDN)
	}
	if host == "" {
		return "", errs.New("ingress has no custom domain or FQDN")
	}
	if path == "" {
		path = "/version"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "https", Host: host, Path: path}).String(), nil
}

func waitForDeploymentVersion(ctx context.Context, endpoint, expected, field string, timeout, interval time.Duration, out io.Writer) error {
	if field == "" {
		field = "commit"
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	attempt := 1
	var last string
	for {
		got, err := fetchVersionField(ctx, endpoint, field)
		if err != nil {
			last = err.Error()
			if out != nil {
				fmt.Fprintf(out, "attempt %d: %s\n", attempt, last)
			}
		} else {
			last = got
			if out != nil {
				fmt.Fprintf(out, "attempt %d: deployed version %s\n", attempt, got)
			}
			if got == expected {
				return nil
			}
		}
		if time.Now().Add(interval).After(deadline) {
			return errs.Errorf("timed out after %s waiting for %s to report %s=%s (last: %s)",
				timeout, endpoint, field, expected, stringOr(last, "none"))
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errs.Wrap(ctx.Err(), "wait-for-deploy cancelled")
		case <-timer.C:
		}
		attempt++
	}
}

func fetchVersionField(ctx context.Context, endpoint, field string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errs.Errorf("GET %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	return extractVersionField(body, field)
}

func extractVersionField(body []byte, field string) (string, error) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return "", errs.Wrap(err, "parse version JSON")
	}
	v, ok := obj[field]
	if !ok {
		return "", errs.Errorf("missing field %q in version JSON", field)
	}
	switch x := v.(type) {
	case string:
		return x, nil
	default:
		return fmt.Sprint(x), nil
	}
}

func currentGitSHA(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
