package lazurecfg

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvValue_UnmarshalJSON_String(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", `"hello"`, "hello"},
		{"empty string", `""`, ""},
		{"with spaces", `"hello world"`, "hello world"},
		{"numeric-looking string", `"0"`, "0"},
		{"with escapes", `"line1\nline2"`, "line1\nline2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e EnvValue
			if err := json.Unmarshal([]byte(tc.input), &e); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if e.Value != tc.want {
				t.Errorf("Value = %q, want %q", e.Value, tc.want)
			}
			if e.SecretRef != "" {
				t.Errorf("SecretRef = %q, want empty", e.SecretRef)
			}
			if e.IsSecret() {
				t.Errorf("IsSecret() = true, want false")
			}
		})
	}
}

func TestEnvValue_UnmarshalJSON_Secret(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", `{"secret":"nexus-database-url"}`, "nexus-database-url"},
		{"whitespace", `  { "secret" : "foo" }  `, "foo"},
		{"kebab with dashes", `{"secret":"a-b-c-d"}`, "a-b-c-d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e EnvValue
			if err := json.Unmarshal([]byte(tc.input), &e); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if e.SecretRef != tc.want {
				t.Errorf("SecretRef = %q, want %q", e.SecretRef, tc.want)
			}
			if e.Value != "" {
				t.Errorf("Value = %q, want empty", e.Value)
			}
			if !e.IsSecret() {
				t.Errorf("IsSecret() = false, want true")
			}
		})
	}
}

func TestEnvValue_UnmarshalJSON_Errors(t *testing.T) {
	// Only shapes that reach our UnmarshalJSON (Go's outer decoder rejects
	// empty input and malformed JSON before calling us, with its own
	// reasonable messages — not our responsibility to test).
	cases := []struct {
		name       string
		input      string
		wantSubstr string
	}{
		{"number", `42`, "expected string or {secret"},
		{"negative number", `-5`, "expected string or {secret"},
		{"bool true", `true`, "expected string or {secret"},
		{"bool false", `false`, "expected string or {secret"},
		{"array", `[]`, "expected string or {secret"},
		{"string array", `["a","b"]`, "expected string or {secret"},
		{"object missing secret", `{"value":"x"}`, "expected exactly {secret"},
		{"object with extra keys", `{"secret":"x","value":"y"}`, "expected exactly {secret"},
		{"empty object", `{}`, "expected exactly {secret"},
		{"secret non-string", `{"secret":42}`, "must be a string"},
		{"secret null", `{"secret":null}`, "must be a string"},
		{"secret empty", `{"secret":""}`, "must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e EnvValue
			err := json.Unmarshal([]byte(tc.input), &e)
			if err == nil {
				t.Fatalf("expected error, got nil (parsed: %+v)", e)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestEnvValue_InMap exercises the primary use case: map[string]*EnvValue
// where nil entries model the null-delete semantic for merge_env.
func TestEnvValue_InMap(t *testing.T) {
	input := `{
		"FOO": "plain",
		"BAR": {"secret": "my-secret"},
		"BAZ": null
	}`
	var m map[string]*EnvValue
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := m["FOO"]; got == nil || got.Value != "plain" || got.IsSecret() {
		t.Errorf("FOO = %+v, want {Value: plain}", got)
	}
	if got := m["BAR"]; got == nil || !got.IsSecret() || got.SecretRef != "my-secret" {
		t.Errorf("BAR = %+v, want {SecretRef: my-secret}", got)
	}
	if got, ok := m["BAZ"]; !ok {
		t.Errorf("BAZ key missing")
	} else if got != nil {
		t.Errorf("BAZ = %+v, want nil pointer (null-delete)", got)
	}
}

// TestEnvValue_IsSecret covers the helper on nil and zero-value receivers.
func TestEnvValue_IsSecret(t *testing.T) {
	var nilE *EnvValue
	if nilE.IsSecret() {
		t.Errorf("nil *EnvValue IsSecret() = true, want false")
	}
	zero := &EnvValue{}
	if zero.IsSecret() {
		t.Errorf("zero EnvValue IsSecret() = true, want false")
	}
	val := &EnvValue{Value: "x"}
	if val.IsSecret() {
		t.Errorf("value EnvValue IsSecret() = true, want false")
	}
	sec := &EnvValue{SecretRef: "x"}
	if !sec.IsSecret() {
		t.Errorf("secret EnvValue IsSecret() = false, want true")
	}
}
