package azureapi

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/errs"
)

// logAnalyticsQueryAPIVersion pins the workspace query REST API.
const logAnalyticsQueryAPIVersion = "v1"

// logAnalyticsBaseURL is the public Log Analytics query endpoint.
// var, not const, so tests can point it at an httptest server.
var logAnalyticsBaseURL = "https://api.loganalytics.io"

// LogAnalyticsRow is one result row from a Kusto query, keyed by
// column name. Values keep their native JSON type (string, float64,
// bool, nil) — callers pick out what they need rather than us
// pre-typing every possible Log Analytics table shape.
type LogAnalyticsRow map[string]any

// QueryLogAnalytics runs a Kusto (KQL) query against a Log Analytics
// workspace and returns the first result table as a slice of rows.
// workspaceID is the workspace's customerId GUID (from the managed
// environment's appLogsConfiguration), not an ARM resource id — the
// query API addresses workspaces by that GUID directly.
func QueryLogAnalytics(ctx context.Context, tokens *TokenProvider, workspaceID, kql string) ([]LogAnalyticsRow, error) {
	tok, err := tokens.LogAnalytics(ctx)
	if err != nil {
		return nil, errs.Wrap(err, "log analytics: token")
	}

	endpoint := fmt.Sprintf("%s/%s/workspaces/%s/query", logAnalyticsBaseURL, logAnalyticsQueryAPIVersion, workspaceID)
	slog.Debug("azureapi: POST log analytics query", "url", endpoint, "query", kql)

	var body struct {
		Tables []struct {
			Columns []struct {
				Name string `json:"name"`
			} `json:"columns"`
			Rows [][]any `json:"rows"`
		} `json:"tables"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	resp, err := req.C().R().
		SetContext(ctx).
		SetBearerAuthToken(tok).
		SetBody(map[string]string{"query": kql}).
		SetSuccessResult(&body).
		Post(endpoint)
	if err != nil {
		return nil, errs.Wrap(err, "log analytics: query")
	}
	if !resp.IsSuccessState() {
		return nil, errs.Errorf("log analytics: query: %s: %s", resp.Status, resp.String())
	}
	if body.Error != nil {
		return nil, errs.Errorf("log analytics: query rejected: %s: %s", body.Error.Code, body.Error.Message)
	}
	if len(body.Tables) == 0 {
		return nil, nil
	}

	table := body.Tables[0]
	rows := make([]LogAnalyticsRow, 0, len(table.Rows))
	for _, raw := range table.Rows {
		row := make(LogAnalyticsRow, len(table.Columns))
		for i, col := range table.Columns {
			if i < len(raw) {
				row[col.Name] = raw[i]
			}
		}
		rows = append(rows, row)
	}
	slog.Debug("azureapi: log analytics query done", "rows", len(rows))
	return rows, nil
}
