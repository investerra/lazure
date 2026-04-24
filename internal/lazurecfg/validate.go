package lazurecfg

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidationResult collects errors and warnings from a manifest validation
// pass. Errors make the manifest non-deployable; warnings are advisory.
type ValidationResult struct {
	Errors   []string
	Warnings []string
}

// HasErrors reports whether any errors were recorded.
func (r *ValidationResult) HasErrors() bool { return len(r.Errors) > 0 }

// Err turns the error list into a single error value for convenient call-
// site handling. Returns nil if there are no errors. Warnings are never
// converted — callers should log them separately.
func (r *ValidationResult) Err() error {
	if !r.HasErrors() {
		return nil
	}
	return fmt.Errorf("validation failed (%d error(s)):\n  - %s",
		len(r.Errors), strings.Join(r.Errors, "\n  - "))
}

func (r *ValidationResult) addError(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *ValidationResult) addWarn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Validate walks a rendered Manifest and collects errors + warnings.
// It validates STRUCTURAL rules that don't require external state
// (e.g. secret-reference presence against the SOPS file is task 697.18's
// verify package, not this).
//
// All rules run; we don't early-exit on the first error so the user sees
// every problem at once.
func Validate(m *Manifest) *ValidationResult {
	r := &ValidationResult{}
	if m == nil {
		r.addError("manifest is nil")
		return r
	}

	validateApp(m, r)
	validateIngress(m, r)
	validateRegistries(m, r)
	validateScale(m, r)
	validateSharedEnv(m, r)
	validateContainers(m, r)
	validateInitContainers(m, r)
	validateMergeEnvs(m, r)
	validateVolumeRefs(m, r)

	return r
}

// ---------- app ----------

func validateApp(m *Manifest, r *ValidationResult) {
	if m.App.Location == "" {
		r.addError("app.location is required")
	}
	if m.App.Name == "" {
		r.addError("app.name is required")
	}
	if m.App.ResourceGroup == "" {
		r.addError("app.resource_group is required")
	}
	if m.App.ManagedEnvironmentID == "" {
		r.addError("app.managed_environment_id is required")
	}
	if m.App.Identity == "" {
		r.addError("app.identity is required")
	}
}

// ---------- ingress ----------

func validateIngress(m *Manifest, r *ValidationResult) {
	if m.Ingress == nil {
		return
	}
	validateIPRestrictions(m.Ingress.IPRestrictions, r)
	validateTraffic(m.Ingress.Traffic, r)
}

func validateIPRestrictions(entries []IPRestriction, r *ValidationResult) {
	if len(entries) == 0 {
		return
	}
	var hasAllow, hasDeny bool
	for i, e := range entries {
		switch e.Action {
		case "allow":
			hasAllow = true
		case "deny":
			hasDeny = true
		default:
			r.addError("ingress.ip_restrictions[%d].action = %q, want 'allow' or 'deny'", i, e.Action)
		}
		if e.CIDR == "" {
			r.addError("ingress.ip_restrictions[%d].cidr is required", i)
		}
	}
	if hasAllow && hasDeny {
		r.addError("ingress.ip_restrictions must be uniformly 'allow' OR uniformly 'deny' (Azure limitation)")
	}
}

func validateTraffic(t *Traffic, r *ValidationResult) {
	if t == nil {
		return
	}
	// "traffic: {}" (both nil) is a valid shorthand meaning 100% latest.
	// Only enforce the weight sum when both entries are actually present.
	if t.Latest != nil && t.Previous != nil {
		if t.Latest.Weight+t.Previous.Weight != 100 {
			r.addError("ingress.traffic: latest.weight (%d) + previous.weight (%d) must sum to 100",
				t.Latest.Weight, t.Previous.Weight)
		}
	}
	if t.Previous != nil && t.Latest == nil {
		r.addError("ingress.traffic.previous cannot be set without latest")
	}
}

// ---------- registries ----------

func validateRegistries(m *Manifest, r *ValidationResult) {
	for i, reg := range m.Registries {
		if reg.Server == "" {
			r.addError("registries[%d].server is required", i)
		}
		hasIdentity := reg.Identity != ""
		hasBasic := reg.Username != "" || reg.PasswordSecret != ""
		if hasIdentity && hasBasic {
			r.addError("registries[%d]: identity and username/password_secret are mutually exclusive (got both)", i)
		}
		if reg.Username != "" && reg.PasswordSecret == "" {
			r.addError("registries[%d]: username set but password_secret missing", i)
		}
		if reg.PasswordSecret != "" && reg.Username == "" {
			r.addError("registries[%d]: password_secret set but username missing", i)
		}
	}
}

// ---------- scale ----------

func validateScale(m *Manifest, r *ValidationResult) {
	s := m.Scale
	if s == nil {
		return
	}
	if s.Min < 0 {
		r.addError("scale.min cannot be negative (got %d)", s.Min)
	}
	if s.Max < s.Min {
		r.addError("scale.max (%d) must be >= scale.min (%d)", s.Max, s.Min)
	}
	for i, rule := range s.Rules {
		validateScaleRule(i, rule, r)
	}
}

func validateScaleRule(i int, rule ScaleRule, r *ValidationResult) {
	if rule.Name == "" {
		r.addError("scale.rules[%d].name is required", i)
	}
	// Exactly one rule type.
	count := 0
	if rule.HTTP != nil {
		count++
	}
	if rule.TCP != nil {
		count++
	}
	if rule.CPU != nil {
		count++
	}
	if rule.Memory != nil {
		count++
	}
	if rule.AzureQueue != nil {
		count++
	}
	if rule.Custom != nil {
		count++
	}
	switch count {
	case 0:
		r.addError("scale.rules[%d] (%q): one of http/tcp/cpu/memory/azure_queue/custom must be set", i, rule.Name)
	case 1:
		// ok
	default:
		r.addError("scale.rules[%d] (%q): exactly one rule type allowed (got %d)", i, rule.Name, count)
	}
	if rule.CPU != nil && rule.CPU.Type != "utilization" && rule.CPU.Type != "average_value" {
		r.addError("scale.rules[%d] (%q): cpu.type = %q, want 'utilization' or 'average_value'", i, rule.Name, rule.CPU.Type)
	}
	if rule.Memory != nil && rule.Memory.Type != "utilization" && rule.Memory.Type != "average_value" {
		r.addError("scale.rules[%d] (%q): memory.type = %q, want 'utilization' or 'average_value'", i, rule.Name, rule.Memory.Type)
	}
	// HTTP/TCP scale rules without a threshold are nonsensical — ACA
	// would accept the config but the rule never fires (threshold=0 is
	// the default state). Reject at manifest-load time instead of
	// emitting empty metadata that silently broke autoscaling.
	if rule.HTTP != nil && rule.HTTP.ConcurrentRequests <= 0 {
		r.addError("scale.rules[%d] (%q): http.concurrent_requests must be > 0 (got %d)", i, rule.Name, rule.HTTP.ConcurrentRequests)
	}
	if rule.TCP != nil && rule.TCP.ConcurrentConnections <= 0 {
		r.addError("scale.rules[%d] (%q): tcp.concurrent_connections must be > 0 (got %d)", i, rule.Name, rule.TCP.ConcurrentConnections)
	}
}

// ---------- env (shared + per-container) ----------

var sensitiveKeyRE = regexp.MustCompile(`(?i)PASSWORD|SECRET|TOKEN|KEY|CREDENTIAL`)

// checkEnvValue flags common mistakes: nil values where they don't belong,
// empty strings, plaintext values that look like sensitive data.
//
// Context string is used in messages ("shared env", "container X env",
// "container X merge_env") so the user can locate the problem.
func checkEnvValue(ctx, key string, v *EnvValue, r *ValidationResult) {
	if v == nil {
		// nil is only meaningful in merge_env — caller handles that case.
		// Reaching here for shared/env means the caller passed a map with
		// a nil entry somewhere else, which shouldn't happen; treat as error.
		r.addError("%s: %q has a nil value; use a string, a {secret: <name>}, or remove the key", ctx, key)
		return
	}
	// Secret ref: no further checks (secret existence is verify.go's job).
	if v.IsSecret() {
		return
	}
	// Plain value.
	if v.Value == "" {
		r.addError("%s: %q has an empty value; remove the key or provide a non-empty value", ctx, key)
		return
	}
	// Plaintext-secret heuristic.
	if sensitiveKeyRE.MatchString(key) {
		r.addWarn("%s: %q looks like a secret but has a plaintext value; consider {secret: <name>} instead", ctx, key)
	}
}

func validateSharedEnv(m *Manifest, r *ValidationResult) {
	for k, v := range m.Env {
		if v == nil {
			r.addError("shared env: %q has a nil value; shared env cannot use null (that's merge_env's delete syntax)", k)
			continue
		}
		checkEnvValue("shared env", k, v, r)
	}
}

// ---------- containers + init_containers ----------

func validateContainers(m *Manifest, r *ValidationResult) {
	names := make(map[string]bool)
	for i := range m.Containers {
		c := &m.Containers[i]
		validateContainerCommon(c, "containers", i, r)
		if c.Name != "" {
			if names[c.Name] {
				r.addError("containers: duplicate name %q", c.Name)
			}
			names[c.Name] = true
		}
	}
}

func validateInitContainers(m *Manifest, r *ValidationResult) {
	names := make(map[string]bool)
	for i := range m.InitContainers {
		c := &m.InitContainers[i]
		validateContainerCommon(c, "init_containers", i, r)
		if c.Probes != nil && (c.Probes.Liveness != nil || c.Probes.Readiness != nil || c.Probes.Startup != nil) {
			r.addError("init_containers[%d] (%q): init containers must not have probes (Azure rejects)", i, c.Name)
		}
		if c.Name != "" {
			if names[c.Name] {
				r.addError("init_containers: duplicate name %q", c.Name)
			}
			names[c.Name] = true
		}
	}
}

// validateContainerCommon runs the checks that apply equally to runtime
// and init containers.
func validateContainerCommon(c *Container, kind string, i int, r *ValidationResult) {
	where := fmt.Sprintf("%s[%d]", kind, i)
	if c.Name == "" {
		r.addError("%s.name is required", where)
	} else {
		where = fmt.Sprintf("%s[%d] (%q)", kind, i, c.Name)
	}
	if c.Image == "" {
		r.addError("%s.image is required", where)
	}

	// env and merge_env are mutually exclusive.
	if c.Env != nil && c.MergeEnv != nil {
		r.addError("%s: env and merge_env are mutually exclusive", where)
	}

	// Per-container env (replace mode): null values are an error (just omit).
	if c.Env != nil {
		for k, v := range c.Env {
			if v == nil {
				r.addError("%s.env: %q has a null value; null is only valid in merge_env (to delete a shared key)", where, k)
				continue
			}
			checkEnvValue(where+".env", k, v, r)
		}
	}

	// merge_env is checked separately in validateMergeEnvs because it
	// needs access to the shared env map.

	if c.Probes != nil {
		validateProbes(where, c.Probes, r)
	}
}

// validateMergeEnvs checks merge_env null-on-missing-key (warn) and the
// plaintext-secret heuristic against the shared env. Split out from
// validateContainerCommon because it needs the full Manifest to look up
// shared keys.
func validateMergeEnvs(m *Manifest, r *ValidationResult) {
	check := func(where string, mergeEnv map[string]*EnvValue) {
		for k, v := range mergeEnv {
			if v == nil {
				if _, inShared := m.Env[k]; !inShared {
					r.addWarn("%s.merge_env: %q is null but not present in shared env (no-op; typo?)", where, k)
				}
				continue
			}
			checkEnvValue(where+".merge_env", k, v, r)
		}
	}
	for i := range m.Containers {
		c := &m.Containers[i]
		if c.MergeEnv != nil {
			check(fmt.Sprintf("containers[%d] (%q)", i, c.Name), c.MergeEnv)
		}
	}
	for i := range m.InitContainers {
		c := &m.InitContainers[i]
		if c.MergeEnv != nil {
			check(fmt.Sprintf("init_containers[%d] (%q)", i, c.Name), c.MergeEnv)
		}
	}
}

// ---------- probes ----------

func validateProbes(where string, p *Probes, r *ValidationResult) {
	if p.Liveness != nil {
		validateProbe(where+".probes.liveness", p.Liveness, r)
	}
	if p.Readiness != nil {
		validateProbe(where+".probes.readiness", p.Readiness, r)
	}
	if p.Startup != nil {
		validateProbe(where+".probes.startup", p.Startup, r)
	}
}

func validateProbe(where string, p *Probe, r *ValidationResult) {
	count := 0
	if p.HTTP != nil {
		count++
	}
	if p.TCP != nil {
		count++
	}
	switch count {
	case 0:
		r.addError("%s: exactly one of http/tcp must be set", where)
	case 1:
		// ok
	default:
		r.addError("%s: only one of http/tcp may be set (got %d)", where, count)
	}
	if p.HTTP != nil {
		if p.HTTP.Path == "" {
			r.addError("%s.http.path is required", where)
		}
		if p.HTTP.Port == 0 {
			r.addError("%s.http.port is required", where)
		}
	}
	if p.TCP != nil && p.TCP.Port == 0 {
		r.addError("%s.tcp.port is required", where)
	}
}

// ---------- volumes ----------

func validateVolumeRefs(m *Manifest, r *ValidationResult) {
	defined := make(map[string]bool, len(m.Volumes))
	usedByMount := make(map[string]bool, len(m.Volumes))

	for i, v := range m.Volumes {
		if v.Name == "" {
			r.addError("volumes[%d].name is required", i)
			continue
		}
		if defined[v.Name] {
			r.addError("volumes: duplicate name %q", v.Name)
		}
		defined[v.Name] = true

		switch v.Type {
		case "empty_dir", "secret":
			// no extra fields required
		case "azure_file", "nfs_azure_file":
			if v.StorageName == "" {
				r.addError("volumes[%d] (%q): type %s requires storage_name", i, v.Name, v.Type)
			}
		case "":
			r.addError("volumes[%d] (%q): type is required", i, v.Name)
		default:
			r.addError("volumes[%d] (%q): unknown type %q (want empty_dir|azure_file|nfs_azure_file|secret)", i, v.Name, v.Type)
		}
	}

	checkMounts := func(where string, mounts []VolumeMount) {
		for i, mnt := range mounts {
			if mnt.Name == "" {
				r.addError("%s.volume_mounts[%d].name is required", where, i)
				continue
			}
			if !defined[mnt.Name] {
				r.addError("%s.volume_mounts[%d]: references undefined volume %q", where, i, mnt.Name)
			} else {
				usedByMount[mnt.Name] = true
			}
			if mnt.Path == "" {
				r.addError("%s.volume_mounts[%d] (%q): path is required", where, i, mnt.Name)
			}
		}
	}
	for i := range m.Containers {
		c := &m.Containers[i]
		checkMounts(fmt.Sprintf("containers[%d] (%q)", i, c.Name), c.VolumeMounts)
	}
	for i := range m.InitContainers {
		c := &m.InitContainers[i]
		checkMounts(fmt.Sprintf("init_containers[%d] (%q)", i, c.Name), c.VolumeMounts)
	}

	for name := range defined {
		if !usedByMount[name] {
			r.addWarn("volumes: %q is defined but never mounted", name)
		}
	}
}
