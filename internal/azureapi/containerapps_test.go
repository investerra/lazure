package azureapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/azurearm"
)

// newMockARMClient wires a ContainerAppsClient at an httptest.Server
// with very short poll delays so tests finish in ms, not seconds.
func newMockARMClient(t *testing.T, handler http.Handler) (*ContainerAppsClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &ContainerAppsClient{
		base:     srv.URL,
		tokens:   newTokenProviderWith(&stubCred{token: "tok-arm"}),
		client:   req.C(),
		delays:   []time.Duration{time.Millisecond, 2 * time.Millisecond},
		maxDelay: 5 * time.Millisecond,
	}, srv
}

// ---------- Get ----------

func TestContainerApps_Get_Success(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if v := r.URL.Query().Get("api-version"); v != armAPIVersion {
			t.Errorf("api-version = %q", v)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok-arm" {
			t.Errorf("Authorization = %q", auth)
		}
		_, _ = w.Write([]byte(`{
			"type": "Microsoft.App/containerApps",
			"location": "switzerlandnorth",
			"name": "api-server",
			"properties": {"managedEnvironmentId": "/x"}
		}`))
	}))

	app, err := c.Get(context.Background(), "sub-1", "rg-1", "api-server")
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "api-server" {
		t.Errorf("name = %q", app.Name)
	}
	if app.Location != "switzerlandnorth" {
		t.Errorf("location = %q", app.Location)
	}
}

func TestContainerApps_Get_NotFound(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"NotFound"}}`, http.StatusNotFound)
	}))

	_, err := c.Get(context.Background(), "sub", "rg", "nope")
	if !errors.Is(err, ErrContainerAppNotFound) {
		t.Errorf("error = %v, want ErrContainerAppNotFound", err)
	}
}

func TestContainerApps_Get_ServerError(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	_, err := c.Get(context.Background(), "sub", "rg", "x")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrContainerAppNotFound) {
		t.Error("5xx should not map to NotFound")
	}
}

// ---------- PutAndWait: synchronous ----------

func TestContainerApps_Put_Synchronous(t *testing.T) {
	// Sync PUT path: PUT returns 200; waitForCompletion follows up
	// with a GET to fetch the read-after-write-consistent state
	// (covers the case where ARM's sync response omits read-only
	// fields like properties.latestRevisionName).
	var sawPut, sawGet bool
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			sawPut = true
			_, _ = w.Write([]byte(`{
				"type": "Microsoft.App/containerApps",
				"name": "api-server",
				"location": "switzerlandnorth",
				"properties": {"managedEnvironmentId": "/x"}
			}`))
		case http.MethodGet:
			sawGet = true
			_, _ = w.Write([]byte(`{
				"type": "Microsoft.App/containerApps",
				"name": "api-server",
				"location": "switzerlandnorth",
				"properties": {"managedEnvironmentId": "/x", "latestRevisionName": "api-server--abc"}
			}`))
		default:
			t.Errorf("unexpected method = %s", r.Method)
		}
	}))

	body := &azurearm.ContainerApp{
		Type: "Microsoft.App/containerApps", Location: "switzerlandnorth", Name: "api-server",
	}
	got, err := c.PutAndWait(context.Background(), "sub", "rg", "api-server", body)
	if err != nil {
		t.Fatal(err)
	}
	if !sawPut || !sawGet {
		t.Errorf("expected PUT then GET, sawPut=%v sawGet=%v", sawPut, sawGet)
	}
	if got.Name != "api-server" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Properties.LatestRevisionName != "api-server--abc" {
		t.Errorf("latestRevisionName = %q, want the value from the post-PUT GET", got.Properties.LatestRevisionName)
	}
}

// ---------- PutAndWait: async + poll ----------

// asyncServer is a helper: returns an httptest.Server where PUT to the
// container-app path returns 202 with an Azure-AsyncOperation header
// that points back at /ops/1. /ops/1 returns InProgress until it's
// been polled `pollsBeforeSuccess` times, then Succeeded. GET to the
// container-app path returns a final-state payload after success.
//
// Encapsulating this lets the tests below stay focused on behaviour
// rather than plumbing.
func asyncServer(t *testing.T, pollsBeforeSuccess int32, finalStatus string) *httptest.Server {
	t.Helper()
	var pollCount int32

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	caPath := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/containerApps/x"
	mux.HandleFunc(caPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.Header().Set("Azure-AsyncOperation", srv.URL+"/ops/1")
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"type":"Microsoft.App/containerApps","name":"x","location":"sn","properties":{"managedEnvironmentId":"/x","latestRevisionName":"x--abc"}}`))
		}
	})
	mux.HandleFunc("/ops/1", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&pollCount, 1)
		if n < pollsBeforeSuccess {
			_, _ = w.Write([]byte(`{"status":"InProgress"}`))
			return
		}
		switch finalStatus {
		case "Failed":
			_, _ = w.Write([]byte(`{"status":"Failed","error":{"code":"BadRequest","message":"invalid image"}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"Succeeded"}`))
		}
	})
	return srv
}

func TestContainerApps_Put_Async_PollsUntilSucceeded(t *testing.T) {
	srv := asyncServer(t, 3, "Succeeded")
	c := &ContainerAppsClient{
		base:   srv.URL,
		tokens: newTokenProviderWith(&stubCred{token: "tok"}),
		client: req.C(),
		delays: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
	}

	got, err := c.PutAndWait(context.Background(), "sub", "rg", "x", &azurearm.ContainerApp{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "x" {
		t.Errorf("unexpected name after async: %q", got.Name)
	}
}

func TestContainerApps_Put_Async_FailedSurfacesError(t *testing.T) {
	srv := asyncServer(t, 1, "Failed")
	c := &ContainerAppsClient{
		base:   srv.URL,
		tokens: newTokenProviderWith(&stubCred{token: "tok"}),
		client: req.C(),
		delays: []time.Duration{time.Millisecond},
	}

	_, err := c.PutAndWait(context.Background(), "sub", "rg", "x", &azurearm.ContainerApp{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Failed") {
		t.Errorf("error should mention Failed status: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid image") {
		t.Errorf("error should include upstream message: %v", err)
	}
}

func TestContainerApps_Put_AsyncHeaderURLIsPolledAsIs(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	caPath := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/containerApps/x"
	mux.HandleFunc(caPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.Header().Set("Azure-AsyncOperation", srv.URL+"/ops/create?api-version=from-header")
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"type":"Microsoft.App/containerApps","name":"x","location":"sn","properties":{"managedEnvironmentId":"/x","latestRevisionName":"x--new","provisioningState":"Succeeded"}}`))
		default:
			t.Errorf("unexpected method = %s", r.Method)
		}
	})
	mux.HandleFunc("/ops/create", func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()["api-version"]
		if len(values) != 1 || values[0] != "from-header" {
			t.Fatalf("api-version query = %v, want only [from-header]", values)
		}
		_, _ = w.Write([]byte(`{"status":"Succeeded"}`))
	})

	c := &ContainerAppsClient{
		base:   srv.URL,
		tokens: newTokenProviderWith(&stubCred{token: "tok"}),
		client: req.C(),
		delays: []time.Duration{time.Millisecond},
	}

	got, err := c.PutAndWait(context.Background(), "sub", "rg", "x", &azurearm.ContainerApp{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Properties.LatestRevisionName != "x--new" {
		t.Errorf("latestRevisionName = %q", got.Properties.LatestRevisionName)
	}
}

func TestContainerApps_Put_LocationHeaderUsesHTTPStatusPolling(t *testing.T) {
	var locationPolls int32
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	caPath := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/containerApps/x"
	mux.HandleFunc(caPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.Header().Set("Location", srv.URL+"/ops/location?api-version=from-header")
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"type":"Microsoft.App/containerApps","name":"x","location":"sn","properties":{"managedEnvironmentId":"/x","latestRevisionName":"x--new","provisioningState":"Succeeded"}}`))
		default:
			t.Errorf("unexpected method = %s", r.Method)
		}
	})
	mux.HandleFunc("/ops/location", func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()["api-version"]
		if len(values) != 1 || values[0] != "from-header" {
			t.Fatalf("api-version query = %v, want only [from-header]", values)
		}
		if atomic.AddInt32(&locationPolls, 1) == 1 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	c := &ContainerAppsClient{
		base:   srv.URL,
		tokens: newTokenProviderWith(&stubCred{token: "tok"}),
		client: req.C(),
		delays: []time.Duration{time.Millisecond, time.Millisecond},
	}

	got, err := c.PutAndWait(context.Background(), "sub", "rg", "x", &azurearm.ContainerApp{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Properties.LatestRevisionName != "x--new" {
		t.Errorf("latestRevisionName = %q", got.Properties.LatestRevisionName)
	}
	if got := atomic.LoadInt32(&locationPolls); got != 2 {
		t.Errorf("location polls = %d, want 2", got)
	}
}

func TestContainerApps_Put_CreatedWithAsyncHeaderPollsUntilFailed(t *testing.T) {
	var pollCount int32
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	caPath := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/containerApps/x"
	mux.HandleFunc(caPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.Header().Set("Azure-AsyncOperation", srv.URL+"/ops/create")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"properties":{"provisioningState":"InProgress"}}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"type":"Microsoft.App/containerApps","name":"x","location":"sn","properties":{"managedEnvironmentId":"/x","latestRevisionName":"x--old","provisioningState":"InProgress"}}`))
		default:
			t.Errorf("unexpected method = %s", r.Method)
		}
	})
	mux.HandleFunc("/ops/create", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pollCount, 1)
		_, _ = w.Write([]byte(`{"status":"Failed","error":{"code":"ContainerAppOperationError","message":"invalid image pull"}}`))
	})

	c := &ContainerAppsClient{
		base:   srv.URL,
		tokens: newTokenProviderWith(&stubCred{token: "tok"}),
		client: req.C(),
		delays: []time.Duration{time.Millisecond},
	}

	_, err := c.PutAndWait(context.Background(), "sub", "rg", "x", &azurearm.ContainerApp{})
	if err == nil {
		t.Fatal("expected failed async operation to fail the PUT")
	}
	if got := atomic.LoadInt32(&pollCount); got != 1 {
		t.Fatalf("async operation polled %d times, want 1", got)
	}
	if !strings.Contains(err.Error(), "invalid image pull") {
		t.Errorf("error should include upstream message: %v", err)
	}
}

func TestContainerApps_Put_CreatedWithoutAsyncHeaderPollsProvisioningState(t *testing.T) {
	var getCount int32
	mux := http.NewServeMux()
	caPath := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/containerApps/x"

	mux.HandleFunc(caPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"properties":{"provisioningState":"InProgress"}}`))
		case http.MethodGet:
			n := atomic.AddInt32(&getCount, 1)
			state := "InProgress"
			if n >= 2 {
				state = "Failed"
			}
			_, _ = w.Write([]byte(`{
				"type":"Microsoft.App/containerApps",
				"name":"x",
				"location":"sn",
				"properties":{
					"managedEnvironmentId":"/x",
					"latestRevisionName":"x--old",
					"provisioningState":"` + state + `"
				}
			}`))
		default:
			t.Errorf("unexpected method = %s", r.Method)
		}
	})

	c, srv := newMockARMClient(t, mux)
	c.base = srv.URL
	c.delays = []time.Duration{time.Millisecond}

	_, err := c.PutAndWait(context.Background(), "sub", "rg", "x", &azurearm.ContainerApp{})
	if err == nil {
		t.Fatal("expected terminal Failed provisioningState to fail the PUT")
	}
	if got := atomic.LoadInt32(&getCount); got != 2 {
		t.Fatalf("GET count = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "provisioning state Failed") {
		t.Errorf("error should mention failed provisioning state: %v", err)
	}
}

// ---------- PutAndWait: 202 without header ----------

func TestContainerApps_Put_202WithoutHeader(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	_, err := c.PutAndWait(context.Background(), "sub", "rg", "x", &azurearm.ContainerApp{})
	if err == nil {
		t.Fatal("expected error when 202 missing async header")
	}
	if !strings.Contains(err.Error(), "Azure-AsyncOperation") {
		t.Errorf("error should mention missing header: %v", err)
	}
}

// ---------- pollAsyncOp: context cancellation ----------

func TestContainerApps_Poll_ContextCanceled(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"InProgress"}`))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(3 * time.Millisecond)
		cancel()
	}()

	err := c.pollAsyncOp(ctx, c.base+"/ops/always-in-progress")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("error should mention cancellation: %v", err)
	}
}

func TestContainerApps_Poll_HTTPErrorDoesNotLoop(t *testing.T) {
	var polls int32
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&polls, 1)
		http.Error(w, `{"error":{"code":"BadRequest","message":"revision failed"}}`, http.StatusBadRequest)
	}))
	c.delays = []time.Duration{time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.pollAsyncOp(ctx, c.base+"/ops/failed")
	if err == nil {
		t.Fatal("expected HTTP 400 from async operation to fail immediately")
	}
	if got := atomic.LoadInt32(&polls); got != 1 {
		t.Fatalf("poll count = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention HTTP 400: %v", err)
	}
	if !strings.Contains(err.Error(), "revision failed") {
		t.Errorf("error should include response body: %v", err)
	}
}

// ---------- pollAsyncOp: unknown status ----------

func TestContainerApps_Poll_UnknownStatusErrors(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"OhNo"}`))
	}))

	err := c.pollAsyncOp(context.Background(), c.base+"/ops/1")
	if err == nil {
		t.Fatal("expected error for unknown status")
	}
	if !strings.Contains(err.Error(), "unknown async status") {
		t.Errorf("error = %v", err)
	}
}
