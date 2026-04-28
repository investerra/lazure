package azureapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/errs"
)

// ActivityLogAPIVersion is the api-version pin for the ARM activity
// log REST namespace. Stable; rarely changes.
const ActivityLogAPIVersion = "2015-04-01"

// ActivityEvent is the lazure-facing subset of an ARM activity log
// entry. The full Azure schema is huge; we surface what users
// actually want to see in `lazure events`: when it happened, what
// operation, success/failure, and who triggered it.
type ActivityEvent struct {
	EventTimestamp time.Time `json:"eventTimestamp"`
	Caller         string    `json:"caller"`
	Level          string    `json:"level"`
	OperationName  struct {
		Value          string `json:"value"`
		LocalizedValue string `json:"localizedValue"`
	} `json:"operationName"`
	Status struct {
		Value          string `json:"value"`
		LocalizedValue string `json:"localizedValue"`
	} `json:"status"`
	ResourceID    string                  `json:"resourceId"`
	CorrelationID string                  `json:"correlationId"`
	OperationID   string                  `json:"operationId"`
	EventDataID   string                  `json:"eventDataId"`
	Properties    ActivityEventProperties `json:"properties"`
}

type ActivityEventProperties struct {
	StatusMessage string `json:"statusMessage,omitempty"`
	Message       string `json:"message,omitempty"`
	Entity        string `json:"entity,omitempty"`
	EventCategory string `json:"eventCategory,omitempty"`
	Hierarchy     string `json:"hierarchy,omitempty"`
}

type activityLogResponse struct {
	Value    []ActivityEvent `json:"value"`
	NextLink string          `json:"nextLink"`
}

// ListActivityEvents fetches recent ARM activity log entries scoped
// to a single container app resource. Returns up to one page of
// results (Azure's default page size, typically 200) — sufficient
// for the "what happened lately" use case `lazure events` covers.
//
// since bounds the query to events newer than that timestamp; pass
// time.Now().Add(-24*time.Hour) for a 24h window. Azure rejects
// queries older than 90 days.
func ListActivityEvents(ctx context.Context, tokens *TokenProvider, sub, resourceID string, since time.Time) ([]ActivityEvent, error) {
	tok, err := tokens.Management(ctx)
	if err != nil {
		return nil, errs.Wrap(err, "activity log: token")
	}

	// Azure's $filter has its own quoting rules: eventTimestamp values
	// are RFC3339 in single quotes, resourceUri likewise. Build the
	// raw filter expression and url.QueryEscape it.
	filter := fmt.Sprintf(
		"eventTimestamp ge '%s' and resourceUri eq '%s'",
		since.UTC().Format(time.RFC3339), resourceID,
	)
	endpoint := fmt.Sprintf(
		"https://management.azure.com/subscriptions/%s/providers/Microsoft.Insights/eventtypes/management/values?api-version=%s&$filter=%s",
		sub, ActivityLogAPIVersion, url.QueryEscape(filter),
	)
	slog.Debug("azureapi: GET activity log", "url", endpoint)

	var page activityLogResponse
	resp, err := req.C().R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+tok).
		SetSuccessResult(&page).
		Get(endpoint)
	if err != nil {
		return nil, errs.Wrap(err, "activity log")
	}
	if !resp.IsSuccessState() {
		return nil, errs.Errorf("activity log: %s: %s", resp.Status, resp.String())
	}
	slog.Debug("azureapi: activity log response",
		"status", resp.StatusCode, "events", len(page.Value), "more", page.NextLink != "")
	return page.Value, nil
}

// ContainerAppResourceID composes the canonical ARM resource id for a
// Container App, which is what the activity log $filter needs to
// scope queries to a specific app.
func ContainerAppResourceID(sub, rg, name string) string {
	// Trim trailing slash from name in case callers passed a path.
	name = strings.TrimRight(name, "/")
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s",
		sub, rg, name,
	)
}
