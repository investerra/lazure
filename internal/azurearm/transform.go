package azurearm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/investerra/lazure/internal/lazurecfg"
)

// TransformOptions carries the inputs for Transform that are NOT in the
// manifest itself. VaultURL comes from sopsio.VaultURL; PreviousRevision
// comes from a GET of the currently-deployed app (empty on first deploy).
type TransformOptions struct {
	VaultURL         string
	PreviousRevision string // optional; used when ingress.traffic.previous is set
}

// Transform converts a rendered + validated Lazure manifest into the ARM
// Container App shape. Caller is responsible for running Validate first;
// Transform does not repeat structural validation.
func Transform(m *lazurecfg.Manifest, opts TransformOptions) (*ContainerApp, error) {
	if m == nil {
		return nil, fmt.Errorf("transform: manifest is nil")
	}

	app := &ContainerApp{
		Type:     "Microsoft.App/containerApps",
		Location: m.App.Location,
		Name:     m.App.Name,
		Identity: buildIdentity(m.App.Identity),
		Properties: ContainerAppProperties{
			ManagedEnvironmentID: m.App.ManagedEnvironmentID,
		},
	}

	cfg, err := buildConfiguration(m, opts)
	if err != nil {
		return nil, err
	}
	app.Properties.Configuration = cfg

	tmpl, err := buildTemplate(m)
	if err != nil {
		return nil, err
	}
	app.Properties.Template = tmpl

	return app, nil
}

// ---------- identity ----------

func buildIdentity(id lazurecfg.Identity) *Identity {
	if id == "" {
		return nil
	}
	return &Identity{
		Type: "UserAssigned",
		UserAssignedIdentities: map[string]UserAssignedDetail{
			string(id): {},
		},
	}
}

// ---------- configuration ----------

func buildConfiguration(m *lazurecfg.Manifest, opts TransformOptions) (Configuration, error) {
	cfg := Configuration{
		Registries: buildRegistries(m),
		Secrets:    buildSecrets(m, opts.VaultURL),
	}

	if m.Ingress != nil {
		cfg.Ingress = buildIngress(m.Ingress, opts.PreviousRevision)
	}

	// active revisions mode: Multiple iff we actually split traffic between
	// latest and a resolvable previous revision. Otherwise Single.
	if m.Ingress != nil && m.Ingress.Traffic != nil &&
		m.Ingress.Traffic.Previous != nil && opts.PreviousRevision != "" {
		cfg.ActiveRevisionsMode = "Multiple"
	} else {
		cfg.ActiveRevisionsMode = "Single"
	}

	return cfg, nil
}

// ---------- registries ----------

func buildRegistries(m *lazurecfg.Manifest) []Registry {
	if len(m.Registries) == 0 {
		return nil
	}
	out := make([]Registry, 0, len(m.Registries))
	for _, r := range m.Registries {
		entry := Registry{Server: r.Server}
		if r.Username != "" {
			// basic-auth path
			entry.Username = r.Username
			entry.PasswordSecretRef = r.PasswordSecret
		} else {
			// managed-identity path; default to app.identity when absent
			id := r.Identity
			if id == "" {
				id = m.App.Identity
			}
			entry.Identity = string(id)
		}
		out = append(out, entry)
	}
	return out
}

// ---------- secrets (auto-generated from references) ----------

// buildSecrets walks the entire manifest collecting every distinct secret
// reference and emits one ARM Secret per name bound to the Key Vault URL
// with app.identity for auth. Uses lazurecfg.CollectSecretRefs so the
// verify package and transform agree on what counts as a "reference."
func buildSecrets(m *lazurecfg.Manifest, vaultURL string) []Secret {
	names := lazurecfg.CollectSecretRefs(m)
	if len(names) == 0 {
		return nil
	}
	base := strings.TrimRight(vaultURL, "/")
	out := make([]Secret, 0, len(names))
	for _, n := range names {
		out = append(out, Secret{
			Name:        n,
			KeyVaultURL: base + "/secrets/" + n,
			Identity:    string(m.App.Identity),
		})
	}
	return out
}

// ---------- ingress ----------

func buildIngress(i *lazurecfg.Ingress, previousRevision string) *Ingress {
	out := &Ingress{
		External:      i.External,
		TargetPort:    i.TargetPort,
		Transport:     i.Transport,     // lazurecfg uses lowercase to match ARM
		AllowInsecure: i.AllowInsecure,
	}

	if i.Cors != nil {
		out.CorsPolicy = &CorsPolicy{
			AllowCredentials: i.Cors.AllowCredentials,
			AllowedHeaders:   i.Cors.AllowedHeaders,
			AllowedMethods:   i.Cors.AllowedMethods,
			AllowedOrigins:   i.Cors.AllowedOrigins,
			ExposeHeaders:    i.Cors.ExposeHeaders,
			MaxAge:           i.Cors.MaxAge,
		}
	}

	if len(i.IPRestrictions) > 0 {
		out.IPSecurityRestrictions = make([]IPSecurityRestriction, 0, len(i.IPRestrictions))
		for _, r := range i.IPRestrictions {
			out.IPSecurityRestrictions = append(out.IPSecurityRestrictions, IPSecurityRestriction{
				Name:           r.Name,
				Description:    r.Description,
				IPAddressRange: r.CIDR,
				Action:         capitalize(r.Action), // allow→Allow, deny→Deny
			})
		}
	}

	if i.Traffic != nil {
		out.Traffic = buildTraffic(i.Traffic, previousRevision)
	}

	return out
}

// buildTraffic converts our {latest, previous} map shape into ARM's array
// shape. `previous: true` is resolved against the current latestRevisionName
// from a GET of the app; if PreviousRevision is empty (first deploy), the
// previous entry is dropped and latest gets 100% of the weight.
func buildTraffic(t *lazurecfg.Traffic, previousRevision string) []TrafficEntry {
	var out []TrafficEntry

	// Handle `traffic: {}` shorthand → 100% latest.
	if t.Latest == nil && t.Previous == nil {
		return []TrafficEntry{{Weight: 100, LatestRevision: true}}
	}

	if t.Latest != nil {
		e := TrafficEntry{
			Weight:         t.Latest.Weight,
			LatestRevision: true,
			Label:          t.Latest.Label,
		}
		out = append(out, e)
	}

	if t.Previous != nil {
		if previousRevision == "" {
			// First deploy: drop previous, give latest 100%. No warn here —
			// callers (cmd/deploy) already WARN with context at the CLI.
			if len(out) > 0 {
				out[0].Weight = 100
			}
		} else {
			out = append(out, TrafficEntry{
				Weight:       t.Previous.Weight,
				RevisionName: previousRevision,
				Label:        t.Previous.Label,
			})
		}
	}

	// Defensive fallback: shouldn't be possible after validate.go (which
	// rejects previous-without-latest), but guarantees we never emit a
	// deployable app with zero traffic entries.
	if len(out) == 0 {
		return []TrafficEntry{{Weight: 100, LatestRevision: true}}
	}

	return out
}

// ---------- template (containers + init + volumes + scale) ----------

func buildTemplate(m *lazurecfg.Manifest) (Template, error) {
	tmpl := Template{
		Scale:   buildScale(m),
		Volumes: buildVolumes(m.Volumes),
	}

	for i := range m.Containers {
		c, err := buildContainer(m, &m.Containers[i], false)
		if err != nil {
			return tmpl, err
		}
		tmpl.Containers = append(tmpl.Containers, c)
	}
	for i := range m.InitContainers {
		c, err := buildContainer(m, &m.InitContainers[i], true)
		if err != nil {
			return tmpl, err
		}
		tmpl.InitContainers = append(tmpl.InitContainers, c)
	}

	return tmpl, nil
}

// ---------- containers ----------

func buildContainer(m *lazurecfg.Manifest, c *lazurecfg.Container, isInit bool) (Container, error) {
	resolved, err := lazurecfg.ResolveEnv(m.Env, c)
	if err != nil {
		return Container{}, err
	}

	out := Container{
		Name:         c.Name,
		Image:        c.Image,
		Command:      c.Command,
		Args:         c.Args,
		WorkingDir:   c.WorkingDir,
		Env:          envVarsSorted(resolved),
		Resources:    buildResources(c.Resources),
		VolumeMounts: buildVolumeMounts(c.VolumeMounts),
	}

	if !isInit {
		out.Probes = buildProbes(c.Probes)
	}
	// validate.go rejects probes on init containers; we just don't emit them.

	return out, nil
}

// envVarsSorted converts the resolved env map to an ARM-style array in
// alphabetical order. Stable ordering = stable diffs in `lazure diff`.
func envVarsSorted(m map[string]*lazurecfg.EnvValue) []EnvVar {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]EnvVar, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		if v == nil {
			continue // defensive; shouldn't happen for a resolved map
		}
		entry := EnvVar{Name: k}
		if v.IsSecret() {
			entry.SecretRef = v.SecretRef
		} else {
			entry.Value = v.Value
		}
		out = append(out, entry)
	}
	return out
}

func buildResources(r *lazurecfg.Resources) *Resources {
	if r == nil {
		return nil
	}
	return &Resources{CPU: r.CPU, Memory: r.Memory}
}

func buildVolumeMounts(mounts []lazurecfg.VolumeMount) []VolumeMount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]VolumeMount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, VolumeMount{
			VolumeName: m.Name,
			MountPath:  m.Path,
			SubPath:    m.SubPath,
		})
	}
	return out
}

// ---------- probes ----------

func buildProbes(p *lazurecfg.Probes) []Probe {
	if p == nil {
		return nil
	}
	var out []Probe
	if p.Liveness != nil {
		out = append(out, buildProbe("Liveness", p.Liveness))
	}
	if p.Readiness != nil {
		out = append(out, buildProbe("Readiness", p.Readiness))
	}
	if p.Startup != nil {
		out = append(out, buildProbe("Startup", p.Startup))
	}
	return out
}

func buildProbe(kind string, p *lazurecfg.Probe) Probe {
	out := Probe{
		Type:                kind,
		InitialDelaySeconds: p.InitialDelay,
		PeriodSeconds:       p.Period,
		TimeoutSeconds:      p.Timeout,
		FailureThreshold:    p.FailureThreshold,
		SuccessThreshold:    p.SuccessThreshold,
	}
	if p.HTTP != nil {
		out.HTTPGet = &HTTPAction{
			Path:   p.HTTP.Path,
			Port:   p.HTTP.Port,
			Scheme: strings.ToUpper(p.HTTP.Scheme), // http → HTTP per ARM
		}
	}
	if p.TCP != nil {
		out.TCPSocket = &TCPAction{Port: p.TCP.Port}
	}
	return out
}

// ---------- volumes ----------

func buildVolumes(vols []lazurecfg.Volume) []Volume {
	if len(vols) == 0 {
		return nil
	}
	out := make([]Volume, 0, len(vols))
	for _, v := range vols {
		out = append(out, Volume{
			Name:         v.Name,
			StorageType:  storageTypeFor(v.Type),
			StorageName:  v.StorageName,
			MountOptions: v.MountOptions,
		})
	}
	return out
}

func storageTypeFor(t string) string {
	switch t {
	case "empty_dir":
		return "EmptyDir"
	case "azure_file":
		return "AzureFile"
	case "nfs_azure_file":
		return "NfsAzureFile"
	case "secret":
		return "Secret"
	}
	return t // defensive: emit as-is; validate.go rejects unknown types
}

// ---------- scale ----------

func buildScale(m *lazurecfg.Manifest) *Scale {
	s := m.Scale
	// Omitted or empty → Lazure default {min: 1, max: 1} (pinned single
	// replica). Prevents accidental scale-to-zero, which is Azure's native
	// default but rarely what you actually want.
	if s == nil || (s.Min == 0 && s.Max == 0 && len(s.Rules) == 0) {
		return &Scale{MinReplicas: 1, MaxReplicas: 1}
	}

	out := &Scale{
		MinReplicas:     s.Min,
		MaxReplicas:     s.Max,
		CooldownPeriod:  s.CooldownPeriod,
		PollingInterval: s.PollingInterval,
	}
	if len(s.Rules) > 0 {
		out.Rules = buildScaleRules(s.Rules, string(m.App.Identity))
	}
	return out
}

func buildScaleRules(rules []lazurecfg.ScaleRule, appIdentity string) []ScaleRule {
	out := make([]ScaleRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, buildScaleRule(r, appIdentity))
	}
	return out
}

// resolveIdentity handles the 'app' keyword + empty-means-app default for
// scaler identity fields.
func resolveIdentity(raw, appIdentity string) string {
	if raw == "" || raw == "app" {
		return appIdentity
	}
	return raw
}

func buildScaleRule(r lazurecfg.ScaleRule, appIdentity string) ScaleRule {
	out := ScaleRule{Name: r.Name}

	switch {
	case r.HTTP != nil:
		meta := map[string]string{}
		if r.HTTP.ConcurrentRequests > 0 {
			meta["concurrentRequests"] = strconv.Itoa(r.HTTP.ConcurrentRequests)
		}
		out.HTTP = &HTTPScaleRule{Metadata: meta}
		applyScalerAuth(r.Auth, appIdentity,
			func(id string) { out.HTTP.Identity = id },
			func(a []ScaleRuleAuth) { out.HTTP.Auth = a })

	case r.TCP != nil:
		meta := map[string]string{}
		if r.TCP.ConcurrentConnections > 0 {
			meta["concurrentConnections"] = strconv.Itoa(r.TCP.ConcurrentConnections)
		}
		out.TCP = &TCPScaleRule{Metadata: meta}
		applyScalerAuth(r.Auth, appIdentity,
			func(id string) { out.TCP.Identity = id },
			func(a []ScaleRuleAuth) { out.TCP.Auth = a })

	case r.CPU != nil:
		out.Custom = &CustomScaleRule{
			Type: "cpu",
			Metadata: map[string]string{
				"type":  r.CPU.Type,
				"value": formatFloat(r.CPU.Value),
			},
		}
		// cpu scalers don't need auth

	case r.Memory != nil:
		out.Custom = &CustomScaleRule{
			Type: "memory",
			Metadata: map[string]string{
				"type":  r.Memory.Type,
				"value": formatFloat(r.Memory.Value),
			},
		}

	case r.AzureQueue != nil:
		q := &QueueScaleRule{
			QueueName:   r.AzureQueue.QueueName,
			QueueLength: r.AzureQueue.QueueLength,
			AccountName: r.AzureQueue.AccountName,
		}
		// Default to app identity when auth omitted — queue scalers require it.
		q.Identity = appIdentity
		applyScalerAuth(r.Auth, appIdentity,
			func(id string) { q.Identity = id },
			func(a []ScaleRuleAuth) { q.Auth = a })
		out.AzureQueue = q

	case r.Custom != nil:
		c := &CustomScaleRule{
			Type:     r.Custom.Type,
			Metadata: r.Custom.Metadata,
		}
		applyScalerAuth(r.Auth, appIdentity,
			func(id string) { c.Identity = id },
			func(a []ScaleRuleAuth) { c.Auth = a })
		out.Custom = c
	}

	return out
}

// applyScalerAuth applies a lazurecfg.RuleAuth block to whichever scaler
// struct needs it. setIdentity / setAuth are callbacks because each scaler
// has its own identity+auth fields (ARM's quirk — they're NOT on ScaleRule).
func applyScalerAuth(auth *lazurecfg.RuleAuth, appIdentity string,
	setIdentity func(string), setAuth func([]ScaleRuleAuth)) {
	if auth == nil {
		return
	}
	if auth.Identity != "" {
		setIdentity(resolveIdentity(auth.Identity, appIdentity))
	}
	if len(auth.Secrets) > 0 {
		arm := make([]ScaleRuleAuth, 0, len(auth.Secrets))
		for _, s := range auth.Secrets {
			arm = append(arm, ScaleRuleAuth{
				TriggerParameter: s.Param,
				SecretRef:        s.Secret,
			})
		}
		setAuth(arm)
	}
}

// ---------- misc helpers ----------

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
