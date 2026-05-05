package azureapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// armAPIVersion pins us to the latest stable Azure ARM Container Apps API.
// Update here and in azurearm/schema.go docs when bumping.
const armAPIVersion = "2026-01-01"

// provisioningPollTimeout caps how long we wait for an ARM async
// operation or post-PUT GET-loop to reach a terminal state. ACA can
// genuinely take 5-10 min on cold-start image pulls; 30 min is the
// outer envelope before we conclude something is stuck.
const provisioningPollTimeout = 30 * time.Minute

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

type ContainerAppState struct {
	App *azurearm.ContainerApp
	Raw map[string]any
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
	state, err := c.GetState(ctx, sub, rg, name)
	if err != nil {
		return nil, err
	}
	return state.App, nil
}

// GetState fetches the current app and returns both the typed Lazure view
// and the raw Azure JSON. The raw body lets deploy detect Azure fields that
// the typed struct would otherwise drop before a full PUT.
func (c *ContainerAppsClient) GetState(ctx context.Context, sub, rg, name string) (*ContainerAppState, error) {
	r, err := c.armRequest(ctx)
	if err != nil {
		return nil, err
	}
	url := c.base + containerAppPath(sub, rg, name)
	slog.Debug("containerapps: GET", "url", url)

	resp, err := r.Get(url)
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
	body, err := resp.ToBytes()
	if err != nil {
		return nil, errs.Wrapf(err, "containerapps: read GET %s/%s", rg, name)
	}
	var app azurearm.ContainerApp
	if err := json.Unmarshal(body, &app); err != nil {
		return nil, errs.Wrapf(err, "containerapps: parse GET %s/%s", rg, name)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errs.Wrapf(err, "containerapps: parse raw GET %s/%s", rg, name)
	}
	return &ContainerAppState{App: &app, Raw: raw}, nil
}

// PutAndWait creates or updates a container app, polling the async
// operation until it completes. Returns the final ContainerApp state
// (GET after success) so callers can read latestRevisionName etc.
//
// Azure responds to ACA PUTs with either:
//   - 200 for completed updates / no-ops
//   - 201 for create/update started
//   - 202 for accepted async operations
//
// Both paths produce the same return value.
func (c *ContainerAppsClient) PutAndWait(ctx context.Context, sub, rg, name string, body *azurearm.ContainerApp) (*azurearm.ContainerApp, error) {
	return c.PutAndWaitPreservingExternalState(ctx, sub, rg, name, body, nil)
}

// PutAndWaitPreservingExternalState creates or updates a container app
// while carrying forward Azure-owned fields that Lazure intentionally
// does not manage declaratively. Today that means ingress.customDomains:
// domains and certificates are configured outside deploy.yml, but a
// full Container App PUT must still include them or Azure removes them.
func (c *ContainerAppsClient) PutAndWaitPreservingExternalState(ctx context.Context, sub, rg, name string, body *azurearm.ContainerApp, live *azurearm.ContainerApp) (*azurearm.ContainerApp, error) {
	preserveExternalState(body, live)

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

func preserveExternalState(body, live *azurearm.ContainerApp) {
	if body == nil || live == nil {
		return
	}
	if body.Properties.Configuration.Ingress == nil || live.Properties.Configuration.Ingress == nil {
		return
	}
	if len(live.Properties.Configuration.Ingress.CustomDomains) == 0 {
		return
	}
	body.Properties.Configuration.Ingress.CustomDomains = append(
		[]azurearm.CustomDomain(nil),
		live.Properties.Configuration.Ingress.CustomDomains...,
	)
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
// PUT and PATCH. On sync (200/201) and async (202) we converge on the
// same final step — GETs against the resource until its provisioning
// state is terminal — so callers always observe a settled,
// read-after-write-consistent view.
//
// We deliberately do NOT decode the sync PUT/PATCH response body for
// the return value: ARM's sync responses sometimes omit read-only
// fields such as properties.latestRevisionName because the revision-
// creation event hasn't propagated to the read model when the PUT
// returns. That manifests as `revision=""` in deploy logs (and the
// "no latestRevisionName after PutAndWait — skipping --wait" warn
// in cmd/deploy.go). The follow-up GET returns the populated state.
func (c *ContainerAppsClient) waitForCompletion(ctx context.Context, sub, rg, name string, resp *req.Response, verb string) (*azurearm.ContainerApp, error) {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
		opURL, statusBody := asyncOperationURL(resp)
		if resp.StatusCode == http.StatusAccepted && opURL == "" {
			return nil, errs.Errorf("containerapps: %s returned 202 but no Azure-AsyncOperation or Location header", verb)
		}
		if opURL != "" {
			slog.Debug("containerapps: polling async op", "url", opURL, "verb", verb, "http", resp.StatusCode)
			if statusBody {
				if err := c.pollAsyncOp(ctx, opURL); err != nil {
					return nil, err
				}
			} else {
				if err := c.pollLocation(ctx, opURL); err != nil {
					return nil, err
				}
			}
		} else {
			slog.Debug("containerapps: response has no async operation header — refetching resource state", "verb", verb, "http", resp.StatusCode)
		}
		app, err := c.Get(ctx, sub, rg, name)
		if err != nil {
			return nil, err
		}
		return c.waitForProvisioningState(ctx, sub, rg, name, app, verb)

	default:
		return nil, errs.Errorf("containerapps: %s %s/%s: %s %s",
			verb, rg, name, resp.Status, resp.String())
	}
}

func asyncOperationURL(resp *req.Response) (string, bool) {
	if resp == nil {
		return "", false
	}
	if opURL := resp.Header.Get("Azure-AsyncOperation"); opURL != "" {
		return opURL, true
	}
	return resp.Header.Get("Location"), false
}

func (c *ContainerAppsClient) waitForProvisioningState(ctx context.Context, sub, rg, name string, app *azurearm.ContainerApp, verb string) (*azurearm.ContainerApp, error) {
	deadline := time.Now().Add(provisioningPollTimeout)
	for i := 0; ; i++ {
		state := app.Properties.ProvisioningState
		switch state {
		case "", "Succeeded":
			return app, nil
		case "Failed", "Canceled", "Cancelled":
			return nil, errs.Errorf("containerapps: %s %s/%s reached provisioning state %s",
				verb, rg, name, state)
		default:
			slog.Debug("containerapps: resource provisioning still in progress",
				"state", state,
				"verb", verb,
				"poll_iteration", i)
		}
		if time.Now().After(deadline) {
			return nil, errs.Errorf("containerapps: %s %s/%s stuck in %q after %s",
				verb, rg, name, state, provisioningPollTimeout)
		}

		var delay time.Duration
		if i < len(c.delays) {
			delay = c.delays[i]
		} else {
			delay = c.maxDelay
		}
		select {
		case <-ctx.Done():
			return nil, errs.Wrap(ctx.Err(), "containerapps: provisioning poll cancelled")
		case <-time.After(delay):
		}

		next, err := c.Get(ctx, sub, rg, name)
		if err != nil {
			return nil, errs.Wrapf(err, "containerapps: provisioning poll: GET %s/%s", rg, name)
		}
		app = next
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
// {2, 3, 5, 8, 13, 21}s then hold at maxDelay (30s); we also cap the
// total wall-clock to provisioningPollTimeout so a stuck Azure
// operation can't hang the CLI forever.
//
// Returns nil on Succeeded; wrapped error on Failed/Canceled.
// Cancellation of ctx stops polling and returns ctx.Err().
func (c *ContainerAppsClient) pollAsyncOp(ctx context.Context, url string) error {
	url = ensureAPIVersion(url)
	deadline := time.Now().Add(provisioningPollTimeout)
	for i := 0; ; i++ {
		if time.Now().After(deadline) {
			return errs.Errorf("containerapps: async operation poll timed out after %s", provisioningPollTimeout)
		}
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

		r, err := c.operationRequest(ctx)
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
		if !resp.IsSuccessState() {
			return errs.Errorf("containerapps: async operation poll failed: %s %s",
				resp.Status, resp.String())
		}

		switch opStatus.Status {
		case "Succeeded":
			return nil
		case "Failed", "Canceled":
			return errs.Errorf("containerapps: async operation %s: %s",
				opStatus.Status, formatAsyncAzureError(opStatus.Error))
		case "InProgress", "Running", "Accepted", "":
			// keep polling
		default:
			return errs.Errorf("containerapps: unknown async status %q", opStatus.Status)
		}
	}
}

func (c *ContainerAppsClient) pollLocation(ctx context.Context, url string) error {
	url = ensureAPIVersion(url)
	deadline := time.Now().Add(provisioningPollTimeout)
	for i := 0; ; i++ {
		if time.Now().After(deadline) {
			return errs.Errorf("containerapps: async location poll timed out after %s", provisioningPollTimeout)
		}
		var delay time.Duration
		if i < len(c.delays) {
			delay = c.delays[i]
		} else {
			delay = c.maxDelay
		}

		select {
		case <-ctx.Done():
			return errs.Wrap(ctx.Err(), "containerapps: location poll cancelled")
		case <-time.After(delay):
		}

		r, err := c.operationRequest(ctx)
		if err != nil {
			return err
		}
		resp, err := r.Get(url)
		if err != nil {
			return errs.Wrap(err, "containerapps: location poll")
		}
		slog.Debug("containerapps: async location",
			"http", resp.StatusCode,
			"poll_iteration", i)

		switch resp.StatusCode {
		case http.StatusOK, http.StatusNoContent:
			return nil
		case http.StatusAccepted:
			continue
		default:
			return errs.Errorf("containerapps: async location poll failed: %s %s",
				resp.Status, resp.String())
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

// operationRequest is for polling Azure-AsyncOperation / Location URLs
// returned in PUT/PATCH async response headers. Those URLs typically
// embed `api-version` themselves (Azure echoes one tied to the
// underlying operation, which can differ from armAPIVersion), so we
// don't re-append it. See pollAndAttachAPIVersion for the safety net
// when Azure omits it entirely.
func (c *ContainerAppsClient) operationRequest(ctx context.Context) (*req.Request, error) {
	tok, err := c.tokens.Management(ctx)
	if err != nil {
		return nil, err
	}
	return c.client.R().
		SetContext(ctx).
		SetBearerAuthToken(tok).
		SetHeader("Content-Type", "application/json"), nil
}

// ensureAPIVersion guarantees the polling URL carries an `api-version`
// query parameter. ARM specifies that Azure-AsyncOperation/Location
// URLs SHOULD include one, but the spec is "should" not "must" and
// we've observed cases where they don't — without one the poll comes
// back 400 InvalidApiVersionParameter. This is a no-op when Azure
// already supplied a version (the common case).
func ensureAPIVersion(rawURL string) string {
	if strings.Contains(rawURL, "api-version=") {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + "api-version=" + armAPIVersion
}

type asyncAzureErrorBody struct {
	Code    string                `json:"code"`
	Message string                `json:"message"`
	Details []asyncAzureErrorBody `json:"details"`
}

func formatAsyncAzureError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(no error details returned by Azure)"
	}

	var body asyncAzureErrorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return string(raw)
	}

	parts := collectAsyncAzureErrorMessages(body)
	if len(parts) == 0 {
		return string(raw)
	}
	return strings.Join(parts, "; ")
}

func collectAsyncAzureErrorMessages(body asyncAzureErrorBody) []string {
	var parts []string
	if body.Message != "" {
		if body.Code != "" {
			parts = append(parts, body.Code+": "+body.Message)
		} else {
			parts = append(parts, body.Message)
		}
	}
	for _, detail := range body.Details {
		parts = append(parts, collectAsyncAzureErrorMessages(detail)...)
	}
	return parts
}
