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
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = w.Write([]byte(`{
			"type": "Microsoft.App/containerApps",
			"name": "api-server",
			"location": "switzerlandnorth",
			"properties": {"managedEnvironmentId": "/x"}
		}`))
	}))

	body := &azurearm.ContainerApp{
		Type: "Microsoft.App/containerApps", Location: "switzerlandnorth", Name: "api-server",
	}
	got, err := c.PutAndWait(context.Background(), "sub", "rg", "api-server", body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "api-server" {
		t.Errorf("name = %q", got.Name)
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
	var srv *httptest.Server

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

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
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
