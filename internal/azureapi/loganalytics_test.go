package azureapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withLogAnalyticsServer points QueryLogAnalytics's package-level base
// URL at a local httptest server for the duration of the test.
func withLogAnalyticsServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := logAnalyticsBaseURL
	logAnalyticsBaseURL = srv.URL
	t.Cleanup(func() { logAnalyticsBaseURL = orig })
}

func TestQueryLogAnalytics_Success(t *testing.T) {
	withLogAnalyticsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/workspaces/ws-guid/query") {
			t.Errorf("path = %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok-la" {
			t.Errorf("Authorization = %q", auth)
		}
		var reqBody struct {
			Query string `json:"query"`
		}
		b, _ := io.ReadAll(r.Body)
		defer func() { _ = r.Body.Close() }()
		if err := json.Unmarshal(b, &reqBody); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(reqBody.Query, "ContainerAppSystemLogs_CL") {
			t.Errorf("query = %q, missing table name", reqBody.Query)
		}
		_, _ = w.Write([]byte(`{
			"tables": [{
				"columns": [{"name": "TimeGenerated"}, {"name": "Log_s"}, {"name": "Reason_s"}],
				"rows": [
					["2026-07-09T10:00:00.0000000Z", "Pulling image", "Pulling"],
					["2026-07-09T10:00:05.0000000Z", "Back-off restarting failed container", "BackOff"]
				]
			}]
		}`))
	})

	tokens := newTokenProviderWith(&stubCred{token: "tok-la"})
	rows, err := QueryLogAnalytics(context.Background(), tokens, "ws-guid", "ContainerAppSystemLogs_CL | take 2")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["Reason_s"] != "Pulling" {
		t.Errorf("rows[0][Reason_s] = %v", rows[0]["Reason_s"])
	}
	if rows[1]["Log_s"] != "Back-off restarting failed container" {
		t.Errorf("rows[1][Log_s] = %v", rows[1]["Log_s"])
	}
}

func TestQueryLogAnalytics_EmptyTables(t *testing.T) {
	withLogAnalyticsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tables": []}`))
	})

	tokens := newTokenProviderWith(&stubCred{token: "tok-la"})
	rows, err := QueryLogAnalytics(context.Background(), tokens, "ws-guid", "ContainerAppSystemLogs_CL")
	if err != nil {
		t.Fatal(err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil", rows)
	}
}

func TestQueryLogAnalytics_QueryRejected(t *testing.T) {
	withLogAnalyticsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error": {"code": "BadArgumentError", "message": "table not found"}}`))
	})

	tokens := newTokenProviderWith(&stubCred{token: "tok-la"})
	_, err := QueryLogAnalytics(context.Background(), tokens, "ws-guid", "NotATable")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "BadArgumentError") {
		t.Errorf("error = %v", err)
	}
}

func TestQueryLogAnalytics_HTTPError(t *testing.T) {
	withLogAnalyticsServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})

	tokens := newTokenProviderWith(&stubCred{token: "tok-la"})
	_, err := QueryLogAnalytics(context.Background(), tokens, "ws-guid", "ContainerAppSystemLogs_CL")
	if err == nil {
		t.Fatal("expected error")
	}
}
