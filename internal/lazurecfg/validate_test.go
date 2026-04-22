package lazurecfg

import (
	"strings"
	"testing"
)

// validManifest returns a minimally-valid manifest for use as a baseline
// in validation tests. Individual tests mutate fields to trigger specific
// rules without having to build a full manifest each time.
func validManifest() *Manifest {
	return &Manifest{
		App: App{
			Name:                 "api-server",
			Location:             "switzerlandnorth",
			ResourceGroup:        "dev-rg",
			ManagedEnvironmentID: "/subscriptions/x/resourceGroups/y/providers/Microsoft.App/managedEnvironments/env",
			Identity:             "/subscriptions/x/resourceGroups/y/providers/Microsoft.ManagedIdentity/userAssignedIdentities/id",
		},
		Containers: []Container{
			{Name: "app", Image: "acr.io/app:v1", Resources: &Resources{CPU: 0.5, Memory: "1Gi"}},
		},
	}
}

func TestValidate_Valid(t *testing.T) {
	r := Validate(validManifest())
	if r.HasErrors() {
		t.Errorf("expected no errors, got: %v", r.Errors)
	}
}

func TestValidate_Nil(t *testing.T) {
	r := Validate(nil)
	if !r.HasErrors() {
		t.Fatal("expected error for nil manifest")
	}
}

// ---------- app ----------

func TestValidate_App_RequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		mod   func(*Manifest)
		field string
	}{
		{"missing name", func(m *Manifest) { m.App.Name = "" }, "app.name"},
		{"missing location", func(m *Manifest) { m.App.Location = "" }, "app.location"},
		{"missing rg", func(m *Manifest) { m.App.ResourceGroup = "" }, "app.resource_group"},
		{"missing env id", func(m *Manifest) { m.App.ManagedEnvironmentID = "" }, "app.managed_environment_id"},
		{"missing identity", func(m *Manifest) { m.App.Identity = "" }, "app.identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mod(m)
			r := Validate(m)
			if !r.HasErrors() {
				t.Fatal("expected error")
			}
			if !errorsContain(r.Errors, tc.field) {
				t.Errorf("errors %v, want one containing %q", r.Errors, tc.field)
			}
		})
	}
}

// ---------- ingress ----------

func TestValidate_Ingress_IPRestrictions_MixedActions(t *testing.T) {
	m := validManifest()
	m.Ingress = &Ingress{
		External: true,
		IPRestrictions: []IPRestriction{
			{Name: "a", CIDR: "10.0.0.0/8", Action: "allow"},
			{Name: "b", CIDR: "192.168.0.0/16", Action: "deny"},
		},
	}
	r := Validate(m)
	if !errorsContain(r.Errors, "uniformly") {
		t.Errorf("expected 'uniformly' error, got %v", r.Errors)
	}
}

func TestValidate_Ingress_IPRestrictions_UniformAllow(t *testing.T) {
	m := validManifest()
	m.Ingress = &Ingress{
		IPRestrictions: []IPRestriction{
			{Name: "a", CIDR: "10.0.0.0/8", Action: "allow"},
			{Name: "b", CIDR: "172.16.0.0/12", Action: "allow"},
		},
	}
	r := Validate(m)
	if r.HasErrors() {
		t.Errorf("uniform allow should pass, got %v", r.Errors)
	}
}

func TestValidate_Ingress_IPRestrictions_BadAction(t *testing.T) {
	m := validManifest()
	m.Ingress = &Ingress{
		IPRestrictions: []IPRestriction{
			{Name: "a", CIDR: "10.0.0.0/8", Action: "permit"},
		},
	}
	r := Validate(m)
	if !errorsContain(r.Errors, "action") {
		t.Errorf("expected action error, got %v", r.Errors)
	}
}

func TestValidate_Ingress_Traffic_WeightSum(t *testing.T) {
	m := validManifest()
	m.Ingress = &Ingress{
		Traffic: &Traffic{
			Latest:   &TrafficEntry{Weight: 40},
			Previous: &TrafficEntry{Weight: 50},
		},
	}
	r := Validate(m)
	if !errorsContain(r.Errors, "sum to 100") {
		t.Errorf("expected sum=100 error, got %v", r.Errors)
	}
}

func TestValidate_Ingress_Traffic_PreviousWithoutLatest(t *testing.T) {
	m := validManifest()
	m.Ingress = &Ingress{
		Traffic: &Traffic{
			Previous: &TrafficEntry{Weight: 100},
		},
	}
	r := Validate(m)
	if !errorsContain(r.Errors, "previous cannot be set without latest") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Ingress_Traffic_EmptyOK(t *testing.T) {
	m := validManifest()
	m.Ingress = &Ingress{Traffic: &Traffic{}} // shorthand for 100% latest
	r := Validate(m)
	if r.HasErrors() {
		t.Errorf("empty traffic should pass, got %v", r.Errors)
	}
}

// ---------- registries ----------

func TestValidate_Registry_IdentityAndBasicMutex(t *testing.T) {
	m := validManifest()
	m.Registries = []Registry{
		{Server: "acr.io", Identity: "/subs/x", Username: "u", PasswordSecret: "s"},
	}
	r := Validate(m)
	if !errorsContain(r.Errors, "mutually exclusive") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Registry_PartialBasicAuth(t *testing.T) {
	m := validManifest()
	m.Registries = []Registry{{Server: "ghcr.io", Username: "bot"}}
	r := Validate(m)
	if !errorsContain(r.Errors, "password_secret missing") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Registry_PasswordWithoutUsername(t *testing.T) {
	m := validManifest()
	m.Registries = []Registry{{Server: "ghcr.io", PasswordSecret: "pw"}}
	r := Validate(m)
	if !errorsContain(r.Errors, "username missing") {
		t.Errorf("got %v", r.Errors)
	}
}

// ---------- scale ----------

func TestValidate_Scale_MinMaxInvariant(t *testing.T) {
	m := validManifest()
	m.Scale = &Scale{Min: 10, Max: 3}
	r := Validate(m)
	if !errorsContain(r.Errors, ">= scale.min") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Scale_NegativeMin(t *testing.T) {
	m := validManifest()
	m.Scale = &Scale{Min: -1, Max: 1}
	r := Validate(m)
	if !errorsContain(r.Errors, "negative") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Scale_Rule_NoType(t *testing.T) {
	m := validManifest()
	m.Scale = &Scale{Min: 1, Max: 3, Rules: []ScaleRule{{Name: "r1"}}}
	r := Validate(m)
	if !errorsContain(r.Errors, "must be set") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Scale_Rule_TwoTypes(t *testing.T) {
	m := validManifest()
	m.Scale = &Scale{
		Min: 1, Max: 3,
		Rules: []ScaleRule{{
			Name: "r1",
			Http: &HTTPScaler{ConcurrentRequests: 10},
			CPU:  &MetricScaler{Type: "utilization", Value: 70},
		}},
	}
	r := Validate(m)
	if !errorsContain(r.Errors, "exactly one rule type") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Scale_CPU_BadType(t *testing.T) {
	m := validManifest()
	m.Scale = &Scale{Min: 1, Max: 3, Rules: []ScaleRule{{
		Name: "cpu-r", CPU: &MetricScaler{Type: "bogus", Value: 70},
	}}}
	r := Validate(m)
	if !errorsContain(r.Errors, "cpu.type") {
		t.Errorf("got %v", r.Errors)
	}
}

// ---------- env ----------

func TestValidate_SharedEnv_EmptyValueIsError(t *testing.T) {
	m := validManifest()
	m.Env = map[string]*EnvValue{"FOO": {Value: ""}}
	r := Validate(m)
	if !errorsContain(r.Errors, "empty value") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_SharedEnv_NullValueIsError(t *testing.T) {
	m := validManifest()
	m.Env = map[string]*EnvValue{"FOO": nil}
	r := Validate(m)
	if !errorsContain(r.Errors, "nil value") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_SharedEnv_PlaintextSecretWarn(t *testing.T) {
	m := validManifest()
	m.Env = map[string]*EnvValue{"DB_PASSWORD": {Value: "hunter2"}}
	r := Validate(m)
	if r.HasErrors() {
		t.Fatalf("should not error, got %v", r.Errors)
	}
	if !errorsContain(r.Warnings, "plaintext") {
		t.Errorf("expected plaintext warning, got %v", r.Warnings)
	}
}

func TestValidate_SharedEnv_SecretRefOK(t *testing.T) {
	m := validManifest()
	m.Env = map[string]*EnvValue{"DATABASE_URL": {SecretRef: "db-url"}}
	r := Validate(m)
	if r.HasErrors() {
		t.Errorf("secret ref should be valid, got %v", r.Errors)
	}
	if len(r.Warnings) > 0 {
		t.Errorf("no warnings expected, got %v", r.Warnings)
	}
}

// ---------- containers ----------

func TestValidate_Container_RequiredFields(t *testing.T) {
	m := validManifest()
	m.Containers = []Container{{Name: "", Image: ""}}
	r := Validate(m)
	if !errorsContain(r.Errors, ".name is required") {
		t.Errorf("got %v", r.Errors)
	}
	if !errorsContain(r.Errors, ".image is required") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Container_DuplicateNames(t *testing.T) {
	m := validManifest()
	m.Containers = []Container{
		{Name: "app", Image: "x"},
		{Name: "app", Image: "y"},
	}
	r := Validate(m)
	if !errorsContain(r.Errors, "duplicate name") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Container_EnvAndMergeEnvMutex(t *testing.T) {
	m := validManifest()
	m.Containers = []Container{{
		Name:     "app",
		Image:    "x",
		Env:      map[string]*EnvValue{"A": {Value: "1"}},
		MergeEnv: map[string]*EnvValue{"B": {Value: "2"}},
	}}
	r := Validate(m)
	if !errorsContain(r.Errors, "mutually exclusive") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Container_NullInEnvIsError(t *testing.T) {
	m := validManifest()
	m.Containers = []Container{{
		Name: "app", Image: "x",
		Env: map[string]*EnvValue{"A": nil},
	}}
	r := Validate(m)
	if !errorsContain(r.Errors, "null value") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Container_MergeEnv_NullOnMissingKeyWarns(t *testing.T) {
	m := validManifest()
	m.Env = map[string]*EnvValue{"EXISTS": {Value: "x"}}
	m.Containers = []Container{{
		Name: "app", Image: "x",
		MergeEnv: map[string]*EnvValue{
			"EXISTS":      nil, // OK — deletes a real shared key
			"DOES_NOT":    nil, // warn — no-op / typo
			"ANOTHER_ONE": {Value: "ok"},
		},
	}}
	r := Validate(m)
	if r.HasErrors() {
		t.Fatalf("should not error: %v", r.Errors)
	}
	if !errorsContain(r.Warnings, "DOES_NOT") {
		t.Errorf("expected warn about DOES_NOT typo, got %v", r.Warnings)
	}
	if errorsContain(r.Warnings, "EXISTS") {
		t.Errorf("should not warn about valid delete, got %v", r.Warnings)
	}
}

// ---------- init containers ----------

func TestValidate_InitContainer_NoProbes(t *testing.T) {
	m := validManifest()
	m.InitContainers = []Container{{
		Name: "init", Image: "x",
		Probes: &Probes{Liveness: &Probe{HTTP: &HTTPProbe{Path: "/", Port: 80}}},
	}}
	r := Validate(m)
	if !errorsContain(r.Errors, "must not have probes") {
		t.Errorf("got %v", r.Errors)
	}
}

// ---------- probes ----------

func TestValidate_Probe_ExactlyOneType(t *testing.T) {
	m := validManifest()
	m.Containers[0].Probes = &Probes{Liveness: &Probe{
		HTTP: &HTTPProbe{Path: "/", Port: 80},
		TCP:  &TCPProbe{Port: 80},
	}}
	r := Validate(m)
	if !errorsContain(r.Errors, "only one of http/tcp/exec") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Probe_None(t *testing.T) {
	m := validManifest()
	m.Containers[0].Probes = &Probes{Liveness: &Probe{}}
	r := Validate(m)
	if !errorsContain(r.Errors, "exactly one of http/tcp/exec") {
		t.Errorf("got %v", r.Errors)
	}
}

// ---------- volumes ----------

func TestValidate_Volume_AzureFileRequiresStorageName(t *testing.T) {
	m := validManifest()
	m.Volumes = []Volume{{Name: "share", Type: "azure_file"}}
	m.Containers[0].VolumeMounts = []VolumeMount{{Name: "share", Path: "/mnt"}}
	r := Validate(m)
	if !errorsContain(r.Errors, "storage_name") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Volume_UnknownType(t *testing.T) {
	m := validManifest()
	m.Volumes = []Volume{{Name: "x", Type: "unknown"}}
	r := Validate(m)
	if !errorsContain(r.Errors, "unknown type") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_VolumeMount_UndefinedReference(t *testing.T) {
	m := validManifest()
	m.Containers[0].VolumeMounts = []VolumeMount{{Name: "nope", Path: "/mnt"}}
	r := Validate(m)
	if !errorsContain(r.Errors, "undefined volume") {
		t.Errorf("got %v", r.Errors)
	}
}

func TestValidate_Volume_UnusedWarns(t *testing.T) {
	m := validManifest()
	m.Volumes = []Volume{{Name: "orphan", Type: "empty_dir"}}
	r := Validate(m)
	if r.HasErrors() {
		t.Fatalf("should not error: %v", r.Errors)
	}
	if !errorsContain(r.Warnings, "never mounted") {
		t.Errorf("expected unused-volume warning, got %v", r.Warnings)
	}
}

// ---------- helpers ----------

func errorsContain(list []string, substr string) bool {
	for _, e := range list {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

// Verify Err() concatenates all error messages into a single value for
// callers that want to short-circuit on .Err() != nil.
func TestValidationResult_Err(t *testing.T) {
	r := &ValidationResult{Errors: []string{"one", "two", "three"}}
	err := r.Err()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, s := range []string{"one", "two", "three", "3 error"} {
		if !strings.Contains(msg, s) {
			t.Errorf("error %q missing %q", msg, s)
		}
	}
}

func TestValidationResult_Err_Empty(t *testing.T) {
	r := &ValidationResult{}
	if err := r.Err(); err != nil {
		t.Errorf("empty result should return nil error, got %v", err)
	}
}
