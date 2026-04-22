package lazurecfg

import (
	"encoding/json"
	"fmt"
)

// EnvValue is a polymorphic environment-variable value: either a plain string
// value or a reference to a secret by name. Exactly one of Value / SecretRef
// is set after unmarshaling.
//
// The `null`-delete semantic for merge_env is represented at the map level
// via a nil *EnvValue pointer, not a field on this struct. Maps are typed as
// map[string]*EnvValue so encoding/json produces a nil entry for a YAML
// null without ever invoking UnmarshalJSON on it.
type EnvValue struct {
	Value     string `json:"value,omitempty"`
	SecretRef string `json:"secret,omitempty"`
}

// IsSecret reports whether this env value is a secret reference.
func (e *EnvValue) IsSecret() bool {
	return e != nil && e.SecretRef != ""
}

// UnmarshalJSON implements the polymorphic decode: a JSON string becomes
// Value; a JSON object of exactly {"secret": "<name>"} becomes SecretRef;
// anything else is an error.
//
// `sigs.k8s.io/yaml` converts YAML to JSON before struct decoding, so this
// is the hook for both YAML and JSON inputs.
func (e *EnvValue) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("envvalue: empty input")
	}

	// First non-whitespace byte tells us which branch to take.
	b := firstNonSpace(data)

	switch b {
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("envvalue: decoding string: %w", err)
		}
		e.Value = s
		return nil

	case '{':
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("envvalue: decoding object: %w", err)
		}
		sec, ok := m["secret"]
		if !ok || len(m) != 1 {
			return fmt.Errorf("envvalue: expected exactly {secret: <name>}, got keys %v", keysOf(m))
		}
		secStr, ok := sec.(string)
		if !ok {
			return fmt.Errorf("envvalue: {secret: ...} value must be a string, got %T", sec)
		}
		if secStr == "" {
			return fmt.Errorf("envvalue: {secret: ...} value must not be empty")
		}
		e.SecretRef = secStr
		return nil

	default:
		return fmt.Errorf("envvalue: expected string or {secret: <name>}, got %s", string(data))
	}
}

func firstNonSpace(data []byte) byte {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return b
		}
	}
	return 0
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
