package azureapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/errs"
)

// SubscriptionAPIVersion is the api-version pin for the Subscriptions
// REST namespace. Stable; rarely changes.
const SubscriptionAPIVersion = "2022-12-01"

// Subscription is the subset of Azure's GET /subscriptions/{id}
// response that lazure surfaces to users — display name + tenant id +
// state. Mostly used for human-readable confirm prompts ("deploy to
// Production (12345...)") and tenant-mismatch detection.
type Subscription struct {
	ID          string `json:"subscriptionId"`
	DisplayName string `json:"displayName"`
	TenantID    string `json:"tenantId"`
	State       string `json:"state"`
}

// ErrSubscriptionAuth is returned by LookupSubscription when ARM
// rejects the bearer token (HTTP 401). The most common cause is being
// logged in to a different Azure tenant than the one owning the
// target subscription. Distinguished from ErrSubscriptionForbidden so
// callers can format the right hint ("az login --tenant" vs "your
// account doesn't have Reader on this sub").
var ErrSubscriptionAuth = fmt.Errorf("azureapi: subscription token rejected (probable tenant mismatch)")

// ErrSubscriptionForbidden is returned by LookupSubscription on 403:
// the token is valid but the user lacks Reader (or higher) RBAC on
// the subscription.
var ErrSubscriptionForbidden = fmt.Errorf("azureapi: forbidden — caller lacks RBAC on subscription")

// ErrSubscriptionNotFound is returned when ARM can't find the
// subscription — usually a typo in app.identity's resource id, or a
// subscription that's been deleted.
var ErrSubscriptionNotFound = fmt.Errorf("azureapi: subscription not found")

// LookupSubscription fetches the subscription metadata from ARM. Used
// both as a "can I reach this sub at all?" probe (loadAzureTarget,
// doctor) and to print a friendly display name in confirm prompts.
//
// Returns one of the named sentinels on the common failure shapes so
// the cmd layer can produce specific, actionable error messages.
func LookupSubscription(ctx context.Context, tokens *TokenProvider, subID string) (*Subscription, error) {
	tok, err := tokens.Management(ctx)
	if err != nil {
		return nil, errs.Wrap(err, "subscription lookup: token")
	}
	url := fmt.Sprintf("https://management.azure.com/subscriptions/%s?api-version=%s", subID, SubscriptionAPIVersion)
	slog.Debug("azureapi: GET subscription", "url", url)

	var sub Subscription
	resp, err := req.C().R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+tok).
		SetSuccessResult(&sub).
		Get(url)
	if err != nil {
		return nil, errs.Wrap(err, "subscription lookup")
	}
	slog.Debug("azureapi: subscription lookup response", "status", resp.StatusCode)
	switch resp.StatusCode {
	case http.StatusOK:
		return &sub, nil
	case http.StatusUnauthorized:
		return nil, ErrSubscriptionAuth
	case http.StatusForbidden:
		return nil, ErrSubscriptionForbidden
	case http.StatusNotFound:
		return nil, ErrSubscriptionNotFound
	default:
		return nil, errs.Errorf("subscription lookup: %s: %s", resp.Status, resp.String())
	}
}
