package azureapi

import (
	"testing"

	"github.com/investerra/lazure/internal/azurearm"
)

func TestNormalize_NilSafe(t *testing.T) {
	Normalize(nil) // must not panic
}

func TestNormalize_ZeroesReadOnlyFields(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			LatestRevisionName:      "api-server--abc123",
			LatestReadyRevisionName: "api-server--ready",
			LatestRevisionFqdn:      "api-server--ready.example.com",
			ProvisioningState:       "Succeeded",
			RunningStatus:           "Running",
		},
	}
	Normalize(app)
	if app.Properties.LatestRevisionName != "" {
		t.Errorf("LatestRevisionName not zeroed: %q", app.Properties.LatestRevisionName)
	}
	if app.Properties.ProvisioningState != "" {
		t.Errorf("ProvisioningState not zeroed: %q", app.Properties.ProvisioningState)
	}
	if app.Properties.LatestReadyRevisionName != "" {
		t.Errorf("LatestReadyRevisionName not zeroed: %q", app.Properties.LatestReadyRevisionName)
	}
	if app.Properties.LatestRevisionFqdn != "" {
		t.Errorf("LatestRevisionFqdn not zeroed: %q", app.Properties.LatestRevisionFqdn)
	}
	if app.Properties.RunningStatus != "" {
		t.Errorf("RunningStatus not zeroed: %q", app.Properties.RunningStatus)
	}
}

func TestNormalize_SortsSecrets(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Configuration: azurearm.Configuration{
				Secrets: []azurearm.Secret{
					{Name: "zzz"},
					{Name: "aaa"},
					{Name: "mmm"},
				},
			},
		},
	}
	Normalize(app)
	secrets := app.Properties.Configuration.Secrets
	if secrets[0].Name != "aaa" || secrets[1].Name != "mmm" || secrets[2].Name != "zzz" {
		t.Errorf("secrets not sorted: %+v", secrets)
	}
}

func TestNormalize_SortsEnv(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Template: azurearm.Template{
				Containers: []azurearm.Container{{
					Name: "app",
					Env: []azurearm.EnvVar{
						{Name: "ZZZ", Value: "z"},
						{Name: "AAA", Value: "a"},
						{Name: "MMM", Value: "m"},
					},
				}},
			},
		},
	}
	Normalize(app)
	env := app.Properties.Template.Containers[0].Env
	if env[0].Name != "AAA" || env[1].Name != "MMM" || env[2].Name != "ZZZ" {
		t.Errorf("env not sorted: %+v", env)
	}
}

func TestNormalize_SortsProbes(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Template: azurearm.Template{
				Containers: []azurearm.Container{{
					Name: "app",
					Probes: []azurearm.Probe{
						{Type: "Startup"},
						{Type: "Liveness"},
						{Type: "Readiness"},
					},
				}},
			},
		},
	}
	Normalize(app)
	probes := app.Properties.Template.Containers[0].Probes
	if probes[0].Type != "Liveness" || probes[1].Type != "Readiness" || probes[2].Type != "Startup" {
		t.Errorf("probes not sorted: %+v", probes)
	}
}

func TestNormalize_SortsVolumes(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Template: azurearm.Template{
				Volumes: []azurearm.Volume{
					{Name: "zz"},
					{Name: "aa"},
					{Name: "mm"},
				},
				Containers: []azurearm.Container{{
					Name: "app",
					VolumeMounts: []azurearm.VolumeMount{
						{VolumeName: "zz"},
						{VolumeName: "aa"},
					},
				}},
			},
		},
	}
	Normalize(app)
	vols := app.Properties.Template.Volumes
	if vols[0].Name != "aa" || vols[1].Name != "mm" || vols[2].Name != "zz" {
		t.Errorf("volumes not sorted: %+v", vols)
	}
	mounts := app.Properties.Template.Containers[0].VolumeMounts
	if mounts[0].VolumeName != "aa" || mounts[1].VolumeName != "zz" {
		t.Errorf("mounts not sorted: %+v", mounts)
	}
}

func TestNormalize_SortsRegistries(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Configuration: azurearm.Configuration{
				Registries: []azurearm.Registry{
					{Server: "zz.azurecr.io"},
					{Server: "aa.azurecr.io"},
				},
			},
		},
	}
	Normalize(app)
	regs := app.Properties.Configuration.Registries
	if regs[0].Server != "aa.azurecr.io" {
		t.Errorf("registries not sorted: %+v", regs)
	}
}

func TestNormalize_SortsIPRestrictions(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Configuration: azurearm.Configuration{
				Ingress: &azurearm.Ingress{
					IPSecurityRestrictions: []azurearm.IPSecurityRestriction{
						{Name: "zz-rule", IPAddressRange: "10.0.0.0/8", Action: "Allow"},
						{Name: "aa-rule", IPAddressRange: "172.16.0.0/12", Action: "Allow"},
					},
				},
			},
		},
	}
	Normalize(app)
	ips := app.Properties.Configuration.Ingress.IPSecurityRestrictions
	if ips[0].Name != "aa-rule" {
		t.Errorf("IP restrictions not sorted: %+v", ips)
	}
}

func TestNormalize_DropsCustomDomains(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Configuration: azurearm.Configuration{
				Ingress: &azurearm.Ingress{
					CustomDomains: []azurearm.CustomDomain{
						{Name: "api.example.com", BindingType: "SniEnabled", CertificateID: "/certs/api-example-com"},
					},
				},
			},
		},
	}

	Normalize(app)

	if got := app.Properties.Configuration.Ingress.CustomDomains; len(got) != 0 {
		t.Errorf("custom domains should be ignored by diff: %+v", got)
	}
}

func TestNormalize_DropsAzureIngressDefaults(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Configuration: azurearm.Configuration{
				Ingress: &azurearm.Ingress{
					Transport: "Auto",
					Traffic: []azurearm.TrafficEntry{{
						LatestRevision: true,
						Weight:         100,
					}},
				},
			},
		},
	}

	Normalize(app)

	if got := app.Properties.Configuration.Ingress.Transport; got != "" {
		t.Errorf("default transport should be ignored by diff, got %q", got)
	}
	if got := app.Properties.Configuration.Ingress.Traffic; len(got) != 0 {
		t.Errorf("default traffic should be ignored by diff, got %+v", got)
	}
}

func TestNormalize_CanonicalizesAzureReadbackNoise(t *testing.T) {
	app := &azurearm.ContainerApp{
		Location: "Switzerland North",
		Identity: &azurearm.Identity{
			Type: "UserAssigned",
			UserAssignedIdentities: map[string]azurearm.UserAssignedDetail{
				"/subscriptions/SUB/resourcegroups/RG/providers/Microsoft.ManagedIdentity/userAssignedIdentities/App": {
					ClientID: "client",
				},
			},
		},
		Properties: azurearm.ContainerAppProperties{
			Configuration: azurearm.Configuration{
				Registries: []azurearm.Registry{{
					Server:   "acr.azurecr.io",
					Identity: "/subscriptions/SUB/resourcegroups/RG/providers/Microsoft.ManagedIdentity/userAssignedIdentities/App",
				}},
				Secrets: []azurearm.Secret{{
					Name:     "secret",
					Identity: "/subscriptions/SUB/resourcegroups/RG/providers/Microsoft.ManagedIdentity/userAssignedIdentities/App",
				}},
			},
			Template: azurearm.Template{Containers: []azurearm.Container{{
				Name: "app",
				Env: []azurearm.EnvVar{{
					Name:  "LAZURE_FORCE_REDEPLOYED_AT",
					Value: "2026-04-29T10:20:55Z",
				}, {
					Name:  "APP_ENV",
					Value: "dev",
				}},
			}}},
		},
	}

	Normalize(app)

	if app.Location != "switzerlandnorth" {
		t.Errorf("location = %q", app.Location)
	}
	for id, detail := range app.Identity.UserAssignedIdentities {
		if id != "/subscriptions/sub/resourcegroups/rg/providers/microsoft.managedidentity/userassignedidentities/app" {
			t.Errorf("identity key = %q", id)
		}
		if detail.ClientID != "" || detail.PrincipalID != "" {
			t.Errorf("identity detail not stripped: %+v", detail)
		}
	}
	if got := app.Properties.Configuration.Registries[0].Identity; got != "/subscriptions/sub/resourcegroups/rg/providers/microsoft.managedidentity/userassignedidentities/app" {
		t.Errorf("registry identity = %q", got)
	}
	if got := app.Properties.Configuration.Secrets[0].Identity; got != "/subscriptions/sub/resourcegroups/rg/providers/microsoft.managedidentity/userassignedidentities/app" {
		t.Errorf("secret identity = %q", got)
	}
	env := app.Properties.Template.Containers[0].Env
	if len(env) != 1 || env[0].Name != "APP_ENV" {
		t.Errorf("force env not dropped: %+v", env)
	}
}

func TestNormalize_PreservesNonDefaultIngressTransportAndTraffic(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Configuration: azurearm.Configuration{
				Ingress: &azurearm.Ingress{
					Transport: "Http2",
					Traffic: []azurearm.TrafficEntry{{
						LatestRevision: true,
						Weight:         100,
						Label:          "stable",
					}},
				},
			},
		},
	}

	Normalize(app)

	if got := app.Properties.Configuration.Ingress.Transport; got != "http2" {
		t.Errorf("non-default transport should be canonicalized and preserved, got %q", got)
	}
	if got := app.Properties.Configuration.Ingress.Traffic; len(got) != 1 || got[0].Label != "stable" {
		t.Errorf("labeled traffic should be preserved, got %+v", got)
	}
}

// TestNormalize_PreservesDeclaredOrdering confirms fields where order
// matters (containers, init containers, traffic, scale rules) are NOT
// sorted — they stay in the user's declared sequence.
func TestNormalize_PreservesDeclaredOrdering(t *testing.T) {
	app := &azurearm.ContainerApp{
		Properties: azurearm.ContainerAppProperties{
			Configuration: azurearm.Configuration{
				Ingress: &azurearm.Ingress{
					Traffic: []azurearm.TrafficEntry{
						{Weight: 50, LatestRevision: true, Label: "canary"},
						{Weight: 50, RevisionName: "old", Label: "stable"},
					},
				},
			},
			Template: azurearm.Template{
				Containers: []azurearm.Container{
					{Name: "zzz-app"},
					{Name: "aaa-sidecar"},
				},
				InitContainers: []azurearm.Container{
					{Name: "z-init"},
					{Name: "a-init"},
				},
				Scale: &azurearm.Scale{
					Rules: []azurearm.ScaleRule{
						{Name: "zzz-http-rule"},
						{Name: "aaa-cpu-rule"},
					},
				},
			},
		},
	}
	Normalize(app)

	// Containers keep declared order.
	if app.Properties.Template.Containers[0].Name != "zzz-app" {
		t.Errorf("containers reordered: %+v", app.Properties.Template.Containers)
	}
	// Init containers keep declared order (sequence matters for boot).
	if app.Properties.Template.InitContainers[0].Name != "z-init" {
		t.Errorf("init containers reordered: %+v", app.Properties.Template.InitContainers)
	}
	// Traffic keeps declared order (latest first is the transform output).
	if !app.Properties.Configuration.Ingress.Traffic[0].LatestRevision {
		t.Errorf("traffic reordered: %+v", app.Properties.Configuration.Ingress.Traffic)
	}
	// Scale rules keep declared order.
	if app.Properties.Template.Scale.Rules[0].Name != "zzz-http-rule" {
		t.Errorf("scale rules reordered: %+v", app.Properties.Template.Scale.Rules)
	}
}
