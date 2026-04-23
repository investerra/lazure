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
			LatestRevisionName: "api-server--abc123",
			ProvisioningState:  "Succeeded",
		},
	}
	Normalize(app)
	if app.Properties.LatestRevisionName != "" {
		t.Errorf("LatestRevisionName not zeroed: %q", app.Properties.LatestRevisionName)
	}
	if app.Properties.ProvisioningState != "" {
		t.Errorf("ProvisioningState not zeroed: %q", app.Properties.ProvisioningState)
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
