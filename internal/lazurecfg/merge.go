package lazurecfg

import "fmt"

// ResolveEnv computes the effective environment map for a single container
// by combining the top-level shared env block with the container's directive.
//
// Three mutually-exclusive modes:
//
//   - Neither Env nor MergeEnv set: inherit shared verbatim.
//
//   - Env set: REPLACES shared entirely. Only keys listed in the container's
//     env apply. Suitable for sidecars from unrelated images.
//
//   - MergeEnv set: inherits shared AND overlays the container's entries.
//     An entry with a nil *EnvValue (YAML `null`) DELETES that shared key
//     from this container. Non-nil entries override or add.
//
// Having both Env and MergeEnv is a load-time error — callers should have
// already rejected this via validate.go. Guarded here for defence in depth.
//
// The returned map is a fresh copy the caller may mutate without affecting
// the shared or per-container input maps.
func ResolveEnv(shared map[string]*EnvValue, c *Container) (map[string]*EnvValue, error) {
	if c == nil {
		return nil, fmt.Errorf("ResolveEnv: container must not be nil")
	}
	if c.Env != nil && c.MergeEnv != nil {
		return nil, fmt.Errorf("container %q: env and merge_env are mutually exclusive", c.Name)
	}

	switch {
	case c.Env != nil:
		return copyEnv(c.Env), nil

	case c.MergeEnv != nil:
		result := copyEnv(shared)
		for k, v := range c.MergeEnv {
			if v == nil {
				delete(result, k)
				continue
			}
			result[k] = v
		}
		return result, nil

	default:
		return copyEnv(shared), nil
	}
}

// copyEnv returns a shallow copy of an env map. EnvValue entries are
// shared by pointer — they're immutable after parsing, so copying the
// map itself is enough to protect against mutation of the source.
func copyEnv(m map[string]*EnvValue) map[string]*EnvValue {
	out := make(map[string]*EnvValue, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
