package azureapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/azurearm"
)

// ---------- ListRevisions ----------

func TestContainerApps_ListRevisions_Success(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/revisions") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"value": [
				{"name": "app--abc", "properties": {"active": true, "replicas": 3, "trafficWeight": 100, "runningState": "Running"}},
				{"name": "app--def", "properties": {"active": false, "replicas": 0, "trafficWeight": 0, "runningState": "Inactive"}}
			]
		}`))
	}))

	got, err := c.ListRevisions(context.Background(), "sub", "rg", "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d revisions, want 2", len(got))
	}
	if got[0].Name != "app--abc" || !got[0].Properties.Active {
		t.Errorf("first revision: %+v", got[0])
	}
	if got[1].Name != "app--def" || got[1].Properties.RunningState != "Inactive" {
		t.Errorf("second revision: %+v", got[1])
	}
}

func TestContainerApps_ListRevisions_NotFound(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	_, err := c.ListRevisions(context.Background(), "sub", "rg", "missing")
	if !errors.Is(err, ErrContainerAppNotFound) {
		t.Errorf("want ErrContainerAppNotFound, got %v", err)
	}
}

// ---------- ListReplicas ----------

func TestContainerApps_ListReplicas(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/revisions/app--abc/replicas") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"value": [
				{"name": "app--abc-deadbeef", "properties": {"runningState": "Running",
					"containers": [{"name": "app", "ready": true, "started": true, "restartCount": 0}]
				}}
			]
		}`))
	}))

	got, err := c.ListReplicas(context.Background(), "sub", "rg", "app", "app--abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d replicas, want 1", len(got))
	}
	if got[0].Name != "app--abc-deadbeef" {
		t.Errorf("replica name = %q", got[0].Name)
	}
	if !got[0].Properties.Containers[0].Ready {
		t.Errorf("replica container should be ready")
	}
}

// ---------- RestartRevision ----------

func TestContainerApps_RestartRevision(t *testing.T) {
	var called bool
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/revisions/app--abc/restart") {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	}))

	if err := c.RestartRevision(context.Background(), "sub", "rg", "app", "app--abc"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("restart endpoint not hit")
	}
}

func TestContainerApps_RestartRevision_NotFound(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	err := c.RestartRevision(context.Background(), "sub", "rg", "app", "ghost")
	if !errors.Is(err, ErrContainerAppNotFound) {
		t.Errorf("want ErrContainerAppNotFound, got %v", err)
	}
}

// ---------- PatchTrafficAndWait ----------

func TestContainerApps_PatchTraffic_Synchronous(t *testing.T) {
	var gotMethod, gotBody string
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{
			"type": "Microsoft.App/containerApps", "name": "app", "location": "sn",
			"properties": {"managedEnvironmentId": "/x", "configuration": {"activeRevisionsMode": "Multiple"}}
		}`))
	}))

	traffic := []azurearm.TrafficEntry{
		{Weight: 20, LatestRevision: true, Label: "canary"},
		{Weight: 80, RevisionName: "app--old", Label: "stable"},
	}
	result, err := c.PatchTrafficAndWait(context.Background(), "sub", "rg", "app", traffic, "Multiple")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	// Body should carry properties.configuration.ingress.traffic + activeRevisionsMode.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(gotBody), &parsed); err != nil {
		t.Fatal(err)
	}
	cfg := parsed["properties"].(map[string]any)["configuration"].(map[string]any)
	if cfg["activeRevisionsMode"] != "Multiple" {
		t.Errorf("mode = %v", cfg["activeRevisionsMode"])
	}
	ing := cfg["ingress"].(map[string]any)
	if len(ing["traffic"].([]any)) != 2 {
		t.Errorf("traffic entries = %+v", ing["traffic"])
	}
	if result.Properties.Configuration.ActiveRevisionsMode != "Multiple" {
		t.Errorf("result mode = %q", result.Properties.Configuration.ActiveRevisionsMode)
	}
}

// ---------- async shared path ----------

// TestPatchTraffic_AsyncCompletion reuses the same asyncServer helper
// pattern from containerapps_test.go to verify PATCH takes the async
// branch through waitForCompletion just like PUT does.
func TestContainerApps_PatchTraffic_Async(t *testing.T) {
	mux := http.NewServeMux()
	var srv *httptest.Server
	caPath := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/containerApps/x"

	mux.HandleFunc(caPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			w.Header().Set("Azure-AsyncOperation", srv.URL+"/ops/patch")
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"type":"Microsoft.App/containerApps","name":"x","location":"sn","properties":{"managedEnvironmentId":"/x","latestRevisionName":"x--new"}}`))
		}
	})
	mux.HandleFunc("/ops/patch", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"Succeeded"}`))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := &ContainerAppsClient{
		base:   srv.URL,
		tokens: newTokenProviderWith(&stubCred{token: "tok"}),
		client: req.C(),
		delays: []time.Duration{time.Millisecond},
	}

	got, err := c.PatchTrafficAndWait(context.Background(), "sub", "rg", "x",
		[]azurearm.TrafficEntry{{Weight: 100, LatestRevision: true}}, "Single")
	if err != nil {
		t.Fatal(err)
	}
	if got.Properties.LatestRevisionName != "x--new" {
		t.Errorf("post-patch revision = %q", got.Properties.LatestRevisionName)
	}
}
