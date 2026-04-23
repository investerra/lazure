package azureapi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/investerra/lazure/internal/errs"
)

// LogStreamOptions configures a log-stream connection.
type LogStreamOptions struct {
	// Follow keeps the connection open and streams new log lines as the
	// container emits them. Without Follow, Azure sends historical lines
	// then closes.
	Follow bool
	// Tail is the number of historical lines to include before live data.
	// Azure defaults to 20; valid range is 0-300.
	Tail int
}

// GetAuthToken issues the short-lived bearer token used for log
// streaming and interactive exec. Token expires ~1 hour after issue;
// callers should re-fetch if they see a 401.
//
// POST /subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/
//      containerApps/{name}/getAuthtoken?api-version=...
func (c *ContainerAppsClient) GetAuthToken(ctx context.Context, sub, rg, name string) (token string, expires time.Time, err error) {
	r, rerr := c.armRequest(ctx)
	if rerr != nil {
		return "", time.Time{}, rerr
	}
	uurl := c.base + containerAppPath(sub, rg, name) + "/getAuthtoken"
	slog.Debug("containerapps: POST getAuthtoken", "url", uurl)

	var body struct {
		Properties struct {
			Token   string    `json:"token"`
			Expires time.Time `json:"expires"`
		} `json:"properties"`
	}
	resp, err := r.SetSuccessResult(&body).Post(uurl)
	if err != nil {
		return "", time.Time{}, errs.Wrap(err, "containerapps: getAuthToken")
	}
	slog.Debug("containerapps: getAuthtoken response", "status", resp.StatusCode)
	if resp.StatusCode == http.StatusNotFound {
		return "", time.Time{}, ErrContainerAppNotFound
	}
	if !resp.IsSuccessState() {
		return "", time.Time{}, errs.Errorf("containerapps: getAuthToken: %s %s", resp.Status, resp.String())
	}
	return body.Properties.Token, body.Properties.Expires, nil
}

// StreamLogs opens a chunked HTTPS connection to a container's
// LogStreamEndpoint (from ReplicaContainer.LogStreamEndpoint) and
// invokes handler for every line of output. Blocks until EOF (for a
// non-follow stream) or ctx.Done (for follow).
//
// The endpoint URL is pre-scoped to a specific {revision, replica,
// container}; callers pass the one they want. token comes from
// GetAuthToken and must be valid for the call's duration.
//
// Uses net/http directly rather than req because streaming long-lived
// bodies doesn't fit req's request-response-pair model, and we need
// fine control over the reader for line-by-line processing.
func StreamLogs(ctx context.Context, endpoint, token string, opts LogStreamOptions, handler func(line string)) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return errs.Wrapf(err, "logstream: parse endpoint %q", endpoint)
	}
	q := u.Query()
	if opts.Follow {
		q.Set("follow", "true")
	}
	if opts.Tail > 0 {
		q.Set("tailLines", strconv.Itoa(opts.Tail))
	}
	u.RawQuery = q.Encode()

	slog.Debug("logstream: opening", "host", u.Host, "follow", opts.Follow, "tail", opts.Tail)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errs.Wrap(err, "logstream: build request")
	}
	req.Header.Set("Authorization", "Bearer "+token)

	// Timeout=0 disables the overall request deadline — for Follow we
	// need the connection to stay open indefinitely. Context handles
	// cancellation via req.WithContext above.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return errs.Wrap(err, "logstream: connect")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errs.Errorf("logstream: %s: %s", resp.Status, string(body))
	}

	slog.Debug("logstream: connected, reading lines")
	return readLines(ctx, resp.Body, handler)
}

// readLines pumps newline-delimited output from r into handler until
// EOF or ctx.Done. Allocates a per-call Scanner with a 1 MiB initial
// buffer growable to 10 MiB — ACA log lines are usually small, but
// multi-line panics or long JSON logs can blow through the default.
func readLines(ctx context.Context, r io.Reader, handler func(string)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		handler(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		// Cancellation is ctx.Done's job; surface the underlying error
		// otherwise. io.EOF is not an error from Scanner's perspective.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errs.Wrap(err, "logstream: read")
	}
	return nil
}
