package azureapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestGetManagedEnvironment_Success(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/env" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"name": "env",
			"properties": {
				"appLogsConfiguration": {
					"destination": "log-analytics",
					"logAnalyticsConfiguration": {"customerId": "workspace-guid"}
				}
			}
		}`))
	}))

	got, err := c.GetManagedEnvironment(context.Background(), "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/env")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "env" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Properties.AppLogsConfiguration.Destination != "log-analytics" {
		t.Errorf("destination = %q", got.Properties.AppLogsConfiguration.Destination)
	}
	cfg := got.Properties.AppLogsConfiguration.LogAnalyticsConfiguration
	if cfg == nil || cfg.CustomerID != "workspace-guid" {
		t.Errorf("logAnalyticsConfiguration = %+v", cfg)
	}
}

func TestGetManagedEnvironment_NotFound(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))

	_, err := c.GetManagedEnvironment(context.Background(), "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/missing")
	if !errors.Is(err, ErrManagedEnvironmentNotFound) {
		t.Errorf("want ErrManagedEnvironmentNotFound, got %v", err)
	}
}

func TestGetManagedEnvironment_NoLogAnalytics(t *testing.T) {
	c, _ := newMockARMClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name": "env", "properties": {"appLogsConfiguration": {"destination": ""}}}`))
	}))

	got, err := c.GetManagedEnvironment(context.Background(), "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/env")
	if err != nil {
		t.Fatal(err)
	}
	if got.Properties.AppLogsConfiguration.LogAnalyticsConfiguration != nil {
		t.Errorf("expected nil logAnalyticsConfiguration, got %+v", got.Properties.AppLogsConfiguration.LogAnalyticsConfiguration)
	}
}
