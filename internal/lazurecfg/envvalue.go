package lazurecfg

// EnvValue is a polymorphic environment-variable value: either a plain string
// value or a reference to a secret by name. Exactly one of Value / SecretRef
// is set.
//
// The polymorphic YAML decoding (scalar → Value; {secret: X} → SecretRef) is
// added by the UnmarshalYAML method in task lazure-697.3. The `null`-delete
// semantic for merge_env is represented at the map level via a nil *EnvValue
// pointer, not a field on this struct.
type EnvValue struct {
	Value     string `json:"value,omitempty"`
	SecretRef string `json:"secret,omitempty"`
}

// IsSecret reports whether this env value is a secret reference.
func (e *EnvValue) IsSecret() bool {
	return e != nil && e.SecretRef != ""
}
