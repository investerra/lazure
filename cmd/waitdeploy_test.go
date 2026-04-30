package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/investerra/lazure/internal/azurearm"
)

func TestDeploymentVersionURL_PrefersFirstCustomDomain(t *testing.T) {
	app := &azurearm.ContainerApp{}
	app.Properties.Configuration.Ingress = &azurearm.Ingress{
		FQDN: "app.azurecontainerapps.io",
		CustomDomains: []azurearm.CustomDomain{
			{Name: "api.example.com"},
			{Name: "api-alt.example.com"},
		},
	}

	got, err := deploymentVersionURL(app, "/version")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.com/version" {
		t.Fatalf("url = %q", got)
	}
}

func TestDeploymentVersionURL_FallsBackToIngressFQDN(t *testing.T) {
	app := &azurearm.ContainerApp{}
	app.Properties.Configuration.Ingress = &azurearm.Ingress{FQDN: "app.azurecontainerapps.io"}

	got, err := deploymentVersionURL(app, "version")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://app.azurecontainerapps.io/version" {
		t.Fatalf("url = %q", got)
	}
}

func TestDeploymentVersionURL_NoIngress(t *testing.T) {
	_, err := deploymentVersionURL(&azurearm.ContainerApp{}, "/version")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no ingress") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractVersionField(t *testing.T) {
	got, err := extractVersionField([]byte(`{"commit":"abc","build":{"commit":"nested"}}`), "commit")
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc" {
		t.Fatalf("value = %q", got)
	}
}

func TestExtractVersionField_Missing(t *testing.T) {
	_, err := extractVersionField([]byte(`{"version":"abc"}`), "commit")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForDeploymentVersion_SucceedsAfterMismatch(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"commit":"old"}`))
			return
		}
		_, _ = w.Write([]byte(`{"commit":"new"}`))
	}))
	defer srv.Close()

	err := waitForDeploymentVersion(context.Background(), srv.URL, "new", "commit", 200*time.Millisecond, time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestWaitForDeploymentVersion_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"commit":"old"}`))
	}))
	defer srv.Close()

	err := waitForDeploymentVersion(context.Background(), srv.URL, "new", "commit", 5*time.Millisecond, time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
}
