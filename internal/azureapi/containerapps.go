package azureapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// armAPIVersion pins us to the 2024-03-01 Azure ARM Container Apps API.
// Update here and in azurearm/schema.go docs when bumping.
const armAPIVersion = "2024-03-01"

// armBaseURL is the global ARM endpoint. Sovereign clouds use a
// different base (e.g. .us, .cn) but Investerra runs on public Azure
// only, so hardcoded is fine.
const armBaseURL = "https://management.azure.com"

// ErrContainerAppNotFound is returned by Get/PutAndWait when the named
// container app doesn't exist (e.g. before the first deploy).
var ErrContainerAppNotFound = errors.New("containerapps: not found")

// ContainerAppsClient wraps the ARM Container App resource endpoints.
type ContainerAppsClient struct {
	base     string
	tokens   *TokenProvider
	client   *req.Client
	delays   []time.Duration // async poll delays; injectable for tests
	maxDelay time.Duration   // cap between polls
}

// NewContainerAppsClient returns a client pointed at the public Azure
// management endpoint.
func NewContainerAppsClient(tokens *TokenProvider) *ContainerAppsClient {
	return &ContainerAppsClient{
		base:     armBaseURL,
		tokens:   tokens,
		client:   req.C(),
		delays:   []time.Duration{2 * time.Second, 3 * time.Second, 5 * time.Second, 8 * time.Second, 13 * time.Second, 21 * time.Second},
		maxDelay: 30 * time.Second,
	}
}

func containerAppPath(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s",
		sub, rg, name)
}

// Get fetches the current state of a container app. Returns
// ErrContainerAppNotFound if it doesn't exist yet.
func (c *ContainerAppsClient) Get(ctx context.Context, sub, rg, name string) (*azurearm.ContainerApp, error) {
	r, err := c.armRequest(ctx)
	if err != nil {
		return nil, err
	}
	url := c.base + containerAppPath(sub, rg, name)
	slog.Debug("containerapps: GET", "url", url)

	var app azurearm.ContainerApp
	resp, err := r.SetSuccessResult(&app).Get(url)
	if err != nil {
		return nil, errs.Wrapf(err, "containerapps: GET %s/%s", rg, name)
	}
	slog.Debug("containerapps: GET response", "status", resp.StatusCode)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrContainerAppNotFound
	}
	if !resp.IsSuccessState() {
		return nil, errs.Errorf("containerapps: GET %s/%s: %s %s",
			rg, name, resp.Status, resp.String())
	}
	return &app, nil
}

// PutAndWait creates or updates a container app, polling the async
// operation until it completes. Returns the final ContainerApp state
// (GET after success) so callers can read latestRevisionName etc.
//
// Azure responds to ACA PUTs with either:
//   - 200 / 201 for fast sync updates
//   - 202 + Azure-AsyncOperation header for slower deploys
//
// Both paths produce the same return value.
func (c *ContainerAppsClient) PutAndWait(ctx context.Context, sub, rg, name string, body *azurearm.ContainerApp) (*azurearm.ContainerApp, error) {
	r, err := c.armRequest(ctx)
	if err != nil {
		return nil, err
	}
	url := c.base + containerAppPath(sub, rg, name)
	slog.Debug("containerapps: PUT", "url", url)

	resp, err := r.SetBody(body).Put(url)
	if err != nil {
		return nil, errs.Wrapf(err, "containerapps: PUT %s/%s", rg, name)
	}
	slog.Debug("containerapps: PUT response", "status", resp.StatusCode)
	return c.waitForCompletion(ctx, sub, rg, name, resp, "PUT")
}

// PatchTrafficAndWait updates just the traffic distribution on an
// existing container app. Reuses PutAndWait's sync/async handling.
// activeMode should be "Single" or "Multiple" — callers (cmd/rollback)
// pick based on the traffic shape.
func (c *ContainerAppsClient) PatchTrafficAndWait(ctx context.Context, sub, rg, name string, traffic []azurearm.TrafficEntry, activeMode string) (*azurearm.ContainerApp, error) {
	r, err := c.armRequest(ctx)
	if err != nil {
		return nil, err
	}
	url := c.base + containerAppPath(sub, rg, name)

	// Construct a minimal PATCH body carrying only the fields we want
	// to change. ARM's PATCH semantics merge this into the existing
	// resource, so everything else (containers, scale, etc.) stays as is.
	body := map[string]any{
		"properties": map[string]any{
			"configuration": map[string]any{
				"activeRevisionsMode": activeMode,
				"ingress": map[string]any{
					"traffic": traffic,
				},
			},
		},
	}
	slog.Debug("containerapps: PATCH traffic", "url", url, "mode", activeMode, "entries", len(traffic))
	resp, err := r.SetBody(body).Patch(url)
	if err != nil {
		return nil, errs.Wrapf(err, "containerapps: PATCH %s/%s", rg, name)
	}
	slog.Debug("containerapps: PATCH response", "status", resp.StatusCode)
	return c.waitForCompletion(ctx, sub, rg, name, resp, "PATCH")
}

// PatchScaleAndWait updates just the replica bounds on an existing
// container app. Used by `lazure scale` for quick scale-up/down without
// re-PUTing the whole manifest. Same sync/async handling as PutAndWait
// + PatchTrafficAndWait.
//
// Pass minReplicas == 0 to allow scale-to-zero (Azure's default); pass
// the same value for both to pin the replica count. Other manifest
// fields (containers, ingress, scale.rules) are unaffected — ARM's
// PATCH semantics merge.
func (c *ContainerAppsClient) PatchScaleAndWait(ctx context.Context, sub, rg, name string, minReplicas, maxReplicas int) (*azurearm.ContainerApp, error) {
	r, err := c.armRequest(ctx)
	if err != nil {
		return nil, err
	}
	url := c.base + containerAppPath(sub, rg, name)
	body := map[string]any{
		"properties": map[string]any{
			"template": map[string]any{
				"scale": map[string]any{
					"minReplicas": minReplicas,
					"maxReplicas": maxReplicas,
				},
			},
		},
	}
	slog.Debug("containerapps: PATCH scale", "url", url, "min", minReplicas, "max", maxReplicas)
	resp, err := r.SetBody(body).Patch(url)
	if err != nil {
		return nil, errs.Wrapf(err, "containerapps: PATCH scale %s/%s", rg, name)
	}
	slog.Debug("containerapps: PATCH scale response", "status", resp.StatusCode)
	return c.waitForCompletion(ctx, sub, rg, name, resp, "PATCH")
}

// waitForCompletion handles the sync-or-async response dance shared by
// PUT and PATCH. On sync (200/201), decodes the body and returns. On
// async (202), polls Azure-AsyncOperation until Succeeded then fetches
// the final state via Get.
func (c *ContainerAppsClient) waitForCompletion(ctx context.Context, sub, rg, name string, resp *req.Response, verb string) (*azurearm.ContainerApp, error) {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var app azurearm.ContainerApp
		if err := json.Unmarshal(resp.Bytes(), &app); err != nil {
			return nil, errs.Wrapf(err, "containerapps: parse sync %s response", verb)
		}
		return &app, nil

	case http.StatusAccepted:
		opURL := resp.Header.Get("Azure-AsyncOperation")
		if opURL == "" {
			opURL = resp.Header.Get("Location")
		}
		if opURL == "" {
			return nil, errs.Errorf("containerapps: %s returned 202 but no Azure-AsyncOperation or Location header", verb)
		}
		slog.Debug("containerapps: polling async op", "url", opURL, "verb", verb)
		if err := c.pollAsyncOp(ctx, opURL); err != nil {
			return nil, err
		}
		return c.Get(ctx, sub, rg, name)

	default:
		return nil, errs.Errorf("containerapps: %s %s/%s: %s %s",
			verb, rg, name, resp.Status, resp.String())
	}
}

// ListRevisions returns the revisions of a container app. Azure's
// default ordering is newest-first.
func (c *ContainerAppsClient) ListRevisions(ctx context.Context, sub, rg, name string) ([]azurearm.Revision, error) {
	url := c.base + containerAppPath(sub, rg, name) + "/revisions"
	slog.Debug("containerapps: LIST revisions", "url", url)

	var out []azurearm.Revision
	firstPage := true
	for {
		r, err := c.armRequest(ctx)
		if err != nil {
			return nil, err
		}
		if !firstPage {
			// Azure's nextLink already embeds api-version; clearing the
			// query map avoids double-append.
			r.QueryParams = nil
		}
		var page struct {
			Value    []azurearm.Revision `json:"value"`
			NextLink string              `json:"nextLink"`
		}
		resp, err := r.SetSuccessResult(&page).Get(url)
		if err != nil {
			return nil, errs.Wrapf(err, "containerapps: list revisions %s/%s", rg, name)
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrContainerAppNotFound
		}
		if !resp.IsSuccessState() {
			return nil, errs.Errorf("containerapps: list revisions %s/%s: %s", rg, name, resp.Status)
		}
		out = append(out, page.Value...)
		if page.NextLink == "" {
			break
		}
		url = page.NextLink
		firstPage = false
	}
	slog.Debug("containerapps: list revisions done", "count", len(out))
	return out, nil
}

// ListReplicas returns the running replicas of a specific revision.
func (c *ContainerAppsClient) ListReplicas(ctx context.Context, sub, rg, name, revision string) ([]azurearm.Replica, error) {
	r, err := c.armRequest(ctx)
	if err != nil {
		return nil, err
	}
	url := c.base + containerAppPath(sub, rg, name) + "/revisions/" + revision + "/replicas"
	slog.Debug("containerapps: LIST replicas", "url", url)

	var body struct {
		Value []azurearm.Replica `json:"value"`
	}
	resp, err := r.SetSuccessResult(&body).Get(url)
	if err != nil {
		return nil, errs.Wrapf(err, "containerapps: list replicas %s/%s/%s", rg, name, revision)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrContainerAppNotFound
	}
	if !resp.IsSuccessState() {
		return nil, errs.Errorf("containerapps: list replicas: %s", resp.Status)
	}
	slog.Debug("containerapps: list replicas done", "count", len(body.Value))
	return body.Value, nil
}

// RestartRevision triggers a restart of a specific revision. Fire-and-
// forget — Azure responds 202 and the restart proceeds asynchronously.
// We don't poll because there's no user-visible "restart completed"
// signal; the new pod spins up under the same revision.
func (c *ContainerAppsClient) RestartRevision(ctx context.Context, sub, rg, name, revision string) error {
	r, err := c.armRequest(ctx)
	if err != nil {
		return err
	}
	url := c.base + containerAppPath(sub, rg, name) + "/revisions/" + revision + "/restart"
	slog.Debug("containerapps: POST restart", "url", url)

	resp, err := r.Post(url)
	if err != nil {
		return errs.Wrapf(err, "containerapps: restart %s", revision)
	}
	slog.Debug("containerapps: restart response", "status", resp.StatusCode)
	if resp.StatusCode == http.StatusNotFound {
		return ErrContainerAppNotFound
	}
	if !resp.IsSuccessState() {
		return errs.Errorf("containerapps: restart %s: %s %s", revision, resp.Status, resp.String())
	}
	return nil
}

// pollAsyncOp polls an Azure-AsyncOperation URL until it reaches a
// terminal state. Delays follow the Fibonacci-ish sequence
// {2, 3, 5, 8, 13, 21}s then hold at maxDelay (30s).
//
// Returns nil on Succeeded; wrapped error on Failed/Canceled.
// Cancellation of ctx stops polling and returns ctx.Err().
func (c *ContainerAppsClient) pollAsyncOp(ctx context.Context, url string) error {
	for i := 0; ; i++ {
		var delay time.Duration
		if i < len(c.delays) {
			delay = c.delays[i]
		} else {
			delay = c.maxDelay
		}

		select {
		case <-ctx.Done():
			return errs.Wrap(ctx.Err(), "containerapps: poll cancelled")
		case <-time.After(delay):
		}

		r, err := c.armRequest(ctx)
		if err != nil {
			return err
		}
		var opStatus struct {
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error,omitempty"`
		}
		resp, err := r.SetSuccessResult(&opStatus).Get(url)
		if err != nil {
			return errs.Wrap(err, "containerapps: poll")
		}
		slog.Debug("containerapps: async op",
			"status", opStatus.Status,
			"http", resp.StatusCode,
			"poll_iteration", i)

		switch opStatus.Status {
		case "Succeeded":
			return nil
		case "Failed", "Canceled":
			return errs.Errorf("containerapps: async operation %s: %s",
				opStatus.Status, string(opStatus.Error))
		case "InProgress", "Running", "Accepted", "":
			// keep polling
		default:
			return errs.Errorf("containerapps: unknown async status %q", opStatus.Status)
		}
	}
}

// armRequest starts a req.Request authed against management.azure.com
// with the api-version query attached. Same pattern as keyvault's
// authedRequest — the two differ only in scope + api-version.
func (c *ContainerAppsClient) armRequest(ctx context.Context) (*req.Request, error) {
	tok, err := c.tokens.Management(ctx)
	if err != nil {
		return nil, err
	}
	r := c.client.R().
		SetContext(ctx).
		SetBearerAuthToken(tok).
		SetQueryParam("api-version", armAPIVersion).
		SetHeader("Content-Type", "application/json")
	return r, nil
}
