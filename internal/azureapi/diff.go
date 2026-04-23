package azureapi

import (
	"sort"

	"github.com/investerra/lazure/internal/azurearm"
)

// Normalize prepares an ARM ContainerApp for comparison against another.
// It:
//
//   - Zeroes read-only fields that Azure populates on GET. With
//     omitempty on those JSON tags, the marshaled output then contains
//     only fields lazure actually writes.
//
//   - Canonicalizes ordering of arrays that aren't semantically ordered
//     (env vars, secrets, volume mounts, IP restrictions, volumes).
//     GET responses may return these in Azure's internal order rather
//     than the order we PUT; sorting gives a stable shape for diff.
//
// Called by `lazure diff` on BOTH the rendered (expected) body AND the
// deployed (actual) body before computing a unified diff. Applying the
// same transform to both sides is what makes drift detection work.
//
// Unknown fields that Azure populates but we don't model (etag,
// systemData, outbound IPs, FQDN, customDomainVerificationId, etc.)
// are already dropped by encoding/json's default lenient decoding —
// we don't need to enumerate them.
func Normalize(app *azurearm.ContainerApp) {
	if app == nil {
		return
	}

	app.Properties.LatestRevisionName = ""
	app.Properties.ProvisioningState = ""

	cfg := &app.Properties.Configuration
	sort.Slice(cfg.Secrets, func(i, j int) bool {
		return cfg.Secrets[i].Name < cfg.Secrets[j].Name
	})
	sort.Slice(cfg.Registries, func(i, j int) bool {
		return cfg.Registries[i].Server < cfg.Registries[j].Server
	})
	if cfg.Ingress != nil {
		sort.Slice(cfg.Ingress.IPSecurityRestrictions, func(i, j int) bool {
			return cfg.Ingress.IPSecurityRestrictions[i].Name < cfg.Ingress.IPSecurityRestrictions[j].Name
		})
		// Traffic entries: declared order matters (UI shows them in that
		// order), don't sort.
	}

	tmpl := &app.Properties.Template
	for i := range tmpl.Containers {
		normalizeEnv(tmpl.Containers[i].Env)
		normalizeProbes(tmpl.Containers[i].Probes)
		normalizeVolumeMounts(tmpl.Containers[i].VolumeMounts)
	}
	for i := range tmpl.InitContainers {
		normalizeEnv(tmpl.InitContainers[i].Env)
		normalizeVolumeMounts(tmpl.InitContainers[i].VolumeMounts)
	}
	sort.Slice(tmpl.Volumes, func(i, j int) bool {
		return tmpl.Volumes[i].Name < tmpl.Volumes[j].Name
	})
	// Scale rules + containers + initContainers: declared order matters
	// (the init container sequence especially), don't sort.
}

func normalizeEnv(env []azurearm.EnvVar) {
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })
}

func normalizeProbes(p []azurearm.Probe) {
	sort.Slice(p, func(i, j int) bool { return p[i].Type < p[j].Type })
}

func normalizeVolumeMounts(m []azurearm.VolumeMount) {
	sort.Slice(m, func(i, j int) bool { return m[i].VolumeName < m[j].VolumeName })
}
