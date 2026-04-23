package azureapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- GetAuthToken ----------

func TestGetAuthToken_Success(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/getAuthtoken") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"id": "/subscriptions/x/resourceGroups/y/providers/Microsoft.App/containerApps/z",
			"name": "z",
			"properties": {
				"token": "short-lived-log-token",
				"expires": "2026-04-23T12:00:00Z"
			}
		}`))
	}))

	tok, exp, err := c.GetAuthToken(context.Background(), "sub", "rg", "z")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "short-lived-log-token" {
		t.Errorf("token = %q", tok)
	}
	if exp.IsZero() {
		t.Error("expires not parsed")
	}
}

func TestGetAuthToken_NotFound(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))

	_, _, err := c.GetAuthToken(context.Background(), "sub", "rg", "missing")
	if !errors.Is(err, ErrContainerAppNotFound) {
		t.Errorf("want ErrContainerAppNotFound, got %v", err)
	}
}

// ---------- StreamLogs ----------

func TestStreamLogs_ReadsLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-log" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("follow") != "true" {
			t.Errorf("follow query not set")
		}
		if r.URL.Query().Get("tailLines") != "50" {
			t.Errorf("tailLines query = %q", r.URL.Query().Get("tailLines"))
		}
		// Stream a few lines and close.
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("line one\nline two\nline three\n"))
	}))
	t.Cleanup(srv.Close)

	var got []string
	var mu sync.Mutex
	handler := func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	}

	err := StreamLogs(context.Background(), srv.URL, "tok-log",
		LogStreamOptions{Follow: true, Tail: 50}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(got), got)
	}
	for i, want := range []string{"line one", "line two", "line three"} {
		if got[i] != want {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want)
		}
	}
}

func TestStreamLogs_NonFollowNoQueryParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("follow") != "" {
			t.Errorf("follow should not be set when opts.Follow=false")
		}
		if r.URL.Query().Get("tailLines") != "" {
			t.Errorf("tailLines should not be set when opts.Tail=0")
		}
		_, _ = w.Write([]byte("single line\n"))
	}))
	t.Cleanup(srv.Close)

	var got []string
	err := StreamLogs(context.Background(), srv.URL, "t", LogStreamOptions{}, func(s string) { got = append(got, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "single line" {
		t.Errorf("got = %v", got)
	}
}

func TestStreamLogs_ContextCancel(t *testing.T) {
	// Server that writes a line then hangs indefinitely.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("first line\n"))
		if flusher != nil {
			flusher.Flush()
		}
		// Block until client disconnects.
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	var lines []string
	done := make(chan error, 1)
	go func() {
		done <- StreamLogs(ctx, srv.URL, "t", LogStreamOptions{Follow: true}, func(s string) {
			lines = append(lines, s)
			cancel() // cancel as soon as first line arrives
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs didn't return after context cancel")
	}
	if len(lines) != 1 || lines[0] != "first line" {
		t.Errorf("lines = %v", lines)
	}
}

func TestStreamLogs_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	err := StreamLogs(context.Background(), srv.URL, "t", LogStreamOptions{}, func(s string) {})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") && !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("error = %v", err)
	}
}

func TestStreamLogs_InvalidURL(t *testing.T) {
	err := StreamLogs(context.Background(), "://not-a-url", "t", LogStreamOptions{}, func(s string) {})
	if err == nil {
		t.Fatal("expected error")
	}
}
