package azurearm

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestRoundTrip_MinimalApp confirms the struct set round-trips through
// YAML (which is how `az containerapp update --yaml` expects it) without
// field drift. This catches json-tag typos early.
func TestRoundTrip_MinimalApp(t *testing.T) {
	app := ContainerApp{
		Type:     "Microsoft.App/containerApps",
		Location: "switzerlandnorth",
		Name:     "api-server",
		Identity: &Identity{
			Type: "UserAssigned",
			UserAssignedIdentities: map[string]UserAssignedDetail{
				"/subs/x/rg/y/identities/z": {},
			},
		},
		Properties: ContainerAppProperties{
			ManagedEnvironmentID: "/subs/x/managedEnvironments/env",
			Configuration: Configuration{
				ActiveRevisionsMode: "Single",
				Ingress: &Ingress{
					External:   true,
					TargetPort: 8000,
					Transport:  "auto", // lowercase per ARM spec
					CustomDomains: []CustomDomain{
						{
							Name:          "api.example.com",
							BindingType:   "SniEnabled",
							CertificateID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/managedEnvironments/env/certificates/api-example-com",
						},
					},
				},
			},
			Template: Template{
				Containers: []Container{{
					Name:  "app",
					Image: "acr.io/app:v1",
					Env: []EnvVar{
						{Name: "DEBUG", Value: "0"},
						{Name: "DATABASE_URL", SecretRef: "nexus-database-url"},
					},
					Resources: &Resources{CPU: 0.5, Memory: "1Gi"},
				}},
			},
		},
	}

	out, err := yaml.Marshal(app)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	// Spot-check critical camelCase keys and enum values (mixed casing
	// per ARM: PascalCase for identity.type + activeRevisionsMode, lowercase
	// for transport).
	mustContain(t, got, "type: Microsoft.App/containerApps")
	mustContain(t, got, "location: switzerlandnorth")
	mustContain(t, got, "managedEnvironmentId:")
	mustContain(t, got, "activeRevisionsMode: Single")
	mustContain(t, got, "transport: auto")
	mustContain(t, got, "targetPort: 8000")
	mustContain(t, got, "customDomains:")
	mustContain(t, got, "bindingType: SniEnabled")
	mustContain(t, got, "certificateId:")
	mustContain(t, got, "userAssignedIdentities:")
	mustContain(t, got, "secretRef: nexus-database-url")

	// Round-trip back to struct and verify key fields.
	var round ContainerApp
	if err := yaml.Unmarshal(out, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.Name != "api-server" {
		t.Errorf("name lost in round-trip: %q", round.Name)
	}
	if round.Properties.Configuration.Ingress.TargetPort != 8000 {
		t.Errorf("target port lost: %d", round.Properties.Configuration.Ingress.TargetPort)
	}
	if got := round.Properties.Configuration.Ingress.CustomDomains; len(got) != 1 || got[0].Name != "api.example.com" {
		t.Errorf("custom domains lost in round-trip: %+v", got)
	}
	if len(round.Properties.Template.Containers) != 1 {
		t.Fatalf("containers lost: %d", len(round.Properties.Template.Containers))
	}
	c := round.Properties.Template.Containers[0]
	if c.Env[1].SecretRef != "nexus-database-url" {
		t.Errorf("env[1].secretRef lost: %+v", c.Env[1])
	}
}

func TestRoundTrip_Probes(t *testing.T) {
	app := ContainerApp{
		Properties: ContainerAppProperties{
			Template: Template{
				Containers: []Container{{
					Name: "app", Image: "x",
					Probes: []Probe{
						{
							Type:                "Liveness",
							HTTPGet:             &HTTPAction{Path: "/health", Port: 8000},
							InitialDelaySeconds: 10,
							PeriodSeconds:       30,
						},
						{
							Type:      "Readiness",
							TCPSocket: &TCPAction{Port: 8000},
						},
					},
				}},
			},
		},
	}
	out, err := yaml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	mustContain(t, got, "type: Liveness")
	mustContain(t, got, "type: Readiness")
	mustContain(t, got, "httpGet:")
	mustContain(t, got, "tcpSocket:")
	// ACA does not support exec probes (unlike Kubernetes) — verify we
	// don't accidentally model one.
	if strings.Contains(got, "exec:") {
		t.Error("output contains exec probe — ACA does not support exec probes")
	}
}

func TestRoundTrip_Scale(t *testing.T) {
	app := ContainerApp{
		Properties: ContainerAppProperties{
			Template: Template{
				Scale: &Scale{
					MinReplicas: 1,
					MaxReplicas: 10,
					Rules: []ScaleRule{
						{
							Name: "http-r",
							HTTP: &HTTPScaleRule{Metadata: map[string]string{"concurrentRequests": "10"}},
						},
						{
							Name: "queue-r",
							AzureQueue: &QueueScaleRule{
								QueueName: "tasks", QueueLength: 5, AccountName: "storage",
								Identity: "/subs/x",
								Auth:     []ScaleRuleAuth{{TriggerParameter: "connection", SecretRef: "conn"}},
							},
						},
						{
							Name:   "redis-r",
							Custom: &CustomScaleRule{Type: "redis", Metadata: map[string]string{"address": "x"}},
						},
					},
				},
			},
		},
	}
	out, err := yaml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	// ARM uses minReplicas / maxReplicas / cooldownPeriod / pollingInterval
	mustContain(t, got, "minReplicas: 1")
	mustContain(t, got, "maxReplicas: 10")
	mustContain(t, got, "http:")
	mustContain(t, got, "azureQueue:")
	mustContain(t, got, "custom:")
	// Metadata is string-valued in ARM
	mustContain(t, got, `concurrentRequests: "10"`)
}

func TestRoundTrip_Volumes(t *testing.T) {
	app := ContainerApp{
		Properties: ContainerAppProperties{
			Template: Template{
				Volumes: []Volume{
					{Name: "cache", StorageType: "EmptyDir"},
					{Name: "uploads", StorageType: "AzureFile", StorageName: "invstorage-dev", MountOptions: "cache=none"},
					{Name: "secrets", StorageType: "Secret"},
				},
				Containers: []Container{{
					Name: "app", Image: "x",
					VolumeMounts: []VolumeMount{
						{VolumeName: "cache", MountPath: "/var/cache"},
						{VolumeName: "uploads", MountPath: "/srv/uploads", SubPath: "tenant-a"},
					},
				}},
			},
		},
	}
	out, err := yaml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	mustContain(t, got, "storageType: EmptyDir")
	mustContain(t, got, "storageType: AzureFile")
	mustContain(t, got, "storageName: invstorage-dev")
	mustContain(t, got, "volumeName: cache")
	mustContain(t, got, "mountPath: /srv/uploads")
	mustContain(t, got, "subPath: tenant-a")
}

// TestDecode_IgnoresUnknownFields proves the struct decoder silently drops
// read-only ARM fields (etag, systemData, provisioningState, etc.) that a
// GET response includes. This is critical for the diff command — we don't
// want to maintain every read-only field just to parse GET responses.
func TestDecode_IgnoresUnknownFields(t *testing.T) {
	in := []byte(`
type: Microsoft.App/containerApps
location: switzerlandnorth
name: api-server
properties:
  managedEnvironmentId: /subs/x/envs/y
  provisioningState: Succeeded
  latestRevisionName: api-server--abc123
  customDomainVerificationId: xxx
  outboundIpAddresses: ["1.2.3.4"]
  configuration:
    activeRevisionsMode: Single
  template:
    revisionSuffix: abc
    containers: []
systemData:
  createdBy: me
  createdAt: "2026-04-22T00:00:00Z"
etag: "abc123"
`)
	var app ContainerApp
	if err := yaml.Unmarshal(in, &app); err != nil {
		t.Fatalf("decode should ignore unknown fields: %v", err)
	}
	if app.Name != "api-server" {
		t.Errorf("name = %q", app.Name)
	}
}

func mustContain(t *testing.T, s, needle string) {
	t.Helper()
	if !strings.Contains(s, needle) {
		t.Errorf("output missing %q. full output:\n%s", needle, s)
	}
}
