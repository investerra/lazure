package lazurecfg

import "sort"

// CollectSecretRefs returns every distinct secret name referenced by a
// manifest — across the shared env block, each container's env and
// merge_env, each init container's env and merge_env,
// registries.password_secret, and scale rule auth.secrets.
//
// The result is alphabetically sorted and deduplicated so callers get
// stable output for diffs and can bisect-search for membership.
//
// Used by both azurearm.Transform (to generate the ARM secrets stanza)
// and verify.Secrets (to cross-check against SOPS + Key Vault).
func CollectSecretRefs(m *Manifest) []string {
	if m == nil {
		return nil
	}
	seen := map[string]struct{}{}

	addFromEnv := func(env map[string]*EnvValue) {
		for _, v := range env {
			if v != nil && v.IsSecret() && v.SecretRef != "" {
				seen[v.SecretRef] = struct{}{}
			}
		}
	}

	addFromEnv(m.Env)
	for _, c := range m.Containers {
		addFromEnv(c.Env)
		addFromEnv(c.MergeEnv)
	}
	for _, c := range m.InitContainers {
		addFromEnv(c.Env)
		addFromEnv(c.MergeEnv)
	}

	for _, r := range m.Registries {
		if r.PasswordSecret != "" {
			seen[r.PasswordSecret] = struct{}{}
		}
	}

	if m.Scale != nil {
		for _, rule := range m.Scale.Rules {
			if rule.Auth == nil {
				continue
			}
			for _, s := range rule.Auth.Secrets {
				if s.Secret != "" {
					seen[s.Secret] = struct{}{}
				}
			}
		}
	}

	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
