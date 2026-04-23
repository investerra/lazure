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

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var app azurearm.ContainerApp
		if err := json.Unmarshal(resp.Bytes(), &app); err != nil {
			return nil, errs.Wrap(err, "containerapps: parse sync PUT response")
		}
		return &app, nil

	case http.StatusAccepted:
		opURL := resp.Header.Get("Azure-AsyncOperation")
		if opURL == "" {
			opURL = resp.Header.Get("Location")
		}
		if opURL == "" {
			return nil, errs.New("containerapps: PUT returned 202 but no Azure-AsyncOperation or Location header")
		}
		slog.Debug("containerapps: polling async op", "url", opURL)
		if err := c.pollAsyncOp(ctx, opURL); err != nil {
			return nil, err
		}
		// Success — fetch final state so caller has latestRevisionName etc.
		return c.Get(ctx, sub, rg, name)

	default:
		return nil, errs.Errorf("containerapps: PUT %s/%s: %s %s",
			rg, name, resp.Status, resp.String())
	}
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
