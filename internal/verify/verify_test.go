package verify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/investerra/lazure/internal/lazurecfg"
)

// baseManifest returns a valid minimal manifest. Tests mutate fields
// to trigger specific checks.
func baseManifest() *lazurecfg.Manifest {
	return &lazurecfg.Manifest{
		App: lazurecfg.App{
			Name:                 "api-server",
			Location:             "switzerlandnorth",
			ResourceGroup:        "dev",
			ManagedEnvironmentID: "/subs/x/managedEnvironments/y",
			Identity:             "/subs/x/rg/y/identities/z",
		},
		Containers: []lazurecfg.Container{
			{Name: "app", Image: "acr.io/app:v1", Resources: &lazurecfg.Resources{CPU: 0.5, Memory: "1Gi"}},
		},
	}
}

// fakeKV implements the KeyVault interface with scripted responses.
type fakeKV struct {
	present map[string]bool
	fail    map[string]error
}

func (f *fakeKV) SecretExists(ctx context.Context, name string) (bool, error) {
	if err, ok := f.fail[name]; ok {
		return false, err
	}
	return f.present[name], nil
}

// ---------- Secrets ----------

func TestSecrets_AllRefsPresent(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"DB":    {SecretRef: "db-url"},
		"REDIS": {SecretRef: "redis-url"},
	}
	sops := map[string]string{"db-url": "x", "redis-url": "y"}

	r := Secrets(context.Background(), m, sops, nil)
	if r.HasErrors() {
		t.Errorf("should not error when refs match SOPS: %v", r.Errors)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("should not warn when all SOPS keys are used: %v", r.Warnings)
	}
}

func TestSecrets_MissingFromSOPS(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"DB":       {SecretRef: "db-url"},
		"MISSING":  {SecretRef: "not-in-sops"},
	}
	sops := map[string]string{"db-url": "x"}

	r := Secrets(context.Background(), m, sops, nil)
	if !r.HasErrors() {
		t.Fatal("expected error for missing ref")
	}
	if !errorsContain(r.Errors, "not-in-sops") {
		t.Errorf("errors = %v, want one containing 'not-in-sops'", r.Errors)
	}
	// db-url was used, not-in-sops was not — but not-in-sops isn't in SOPS anyway.
	// Warnings should be empty (nothing unused in SOPS).
	if len(r.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", r.Warnings)
	}
}

func TestSecrets_UnusedSOPSKey(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"DB": {SecretRef: "db-url"},
	}
	sops := map[string]string{
		"db-url":      "x",
		"leftover":    "old",
		"another-one": "stale",
	}

	r := Secrets(context.Background(), m, sops, nil)
	if r.HasErrors() {
		t.Errorf("no errors expected: %v", r.Errors)
	}
	if !errorsContain(r.Warnings, "leftover") || !errorsContain(r.Warnings, "another-one") {
		t.Errorf("warnings = %v, want entries for 'leftover' and 'another-one'", r.Warnings)
	}
}

func TestSecrets_CollectsFromAllSources(t *testing.T) {
	// Refs come from: shared env, container env+merge_env, init container
	// env, registries password_secret, scale rule auth.secrets.
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{"SHARED": {SecretRef: "shared-key"}}
	m.Containers[0].MergeEnv = map[string]*lazurecfg.EnvValue{"MERGE": {SecretRef: "merge-key"}}
	m.InitContainers = []lazurecfg.Container{{
		Name: "init", Image: "x",
		Env: map[string]*lazurecfg.EnvValue{"INIT": {SecretRef: "init-key"}},
	}}
	m.Registries = []lazurecfg.Registry{{Server: "ghcr.io", Username: "bot", PasswordSecret: "registry-key"}}
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 1, Rules: []lazurecfg.ScaleRule{{
		Name: "r", Custom: &lazurecfg.CustomScaler{Type: "redis"},
		Auth: &lazurecfg.RuleAuth{Secrets: []lazurecfg.RuleSecretAuth{
			{Param: "password", Secret: "scale-key"},
		}},
	}}}

	// SOPS is missing the init-key — only that one should error.
	sops := map[string]string{
		"shared-key":   "a",
		"merge-key":    "b",
		"registry-key": "c",
		"scale-key":    "d",
	}

	r := Secrets(context.Background(), m, sops, nil)
	if !errorsContain(r.Errors, "init-key") {
		t.Errorf("expected error for init-key, got %v", r.Errors)
	}
	if errorsContain(r.Errors, "shared-key") ||
		errorsContain(r.Errors, "merge-key") ||
		errorsContain(r.Errors, "registry-key") ||
		errorsContain(r.Errors, "scale-key") {
		t.Errorf("unexpected error for a present key: %v", r.Errors)
	}
}

func TestSecrets_KVMissingErrors(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"DB":      {SecretRef: "db-url"},
		"REDIS":   {SecretRef: "redis-url"},
	}
	sops := map[string]string{"db-url": "x", "redis-url": "y"}
	kv := &fakeKV{present: map[string]bool{"db-url": true, "redis-url": false}}

	r := Secrets(context.Background(), m, sops, kv)
	if !errorsContain(r.Errors, "redis-url") {
		t.Errorf("expected KV-missing error for redis-url, got %v", r.Errors)
	}
	if !errorsContain(r.Errors, "lazure secrets sync") {
		t.Errorf("error should suggest running sync, got %v", r.Errors)
	}
	if errorsContain(r.Errors, "db-url") {
		t.Errorf("db-url is present in KV, should not error: %v", r.Errors)
	}
}

func TestSecrets_KVLookupFailure(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{"DB": {SecretRef: "db-url"}}
	sops := map[string]string{"db-url": "x"}
	kv := &fakeKV{fail: map[string]error{"db-url": errors.New("network timeout")}}

	r := Secrets(context.Background(), m, sops, kv)
	if !errorsContain(r.Errors, "Key Vault check failed") {
		t.Errorf("expected KV-failure error, got %v", r.Errors)
	}
	if !errorsContain(r.Errors, "network timeout") {
		t.Errorf("error should include underlying cause, got %v", r.Errors)
	}
}

func TestSecrets_KVSkippedForSOPSMissingRefs(t *testing.T) {
	// If a ref isn't in SOPS, we already errored — no point asking KV
	// about it (wastes a call AND double-reports).
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{"DB": {SecretRef: "not-in-sops"}}
	sops := map[string]string{}

	kv := &fakeKV{} // fakeKV returns false+nil for unknown keys
	r := Secrets(context.Background(), m, sops, kv)

	// Exactly one error: "missing from SOPS". Not a KV error too.
	if count := countContaining(r.Errors, "not-in-sops"); count != 1 {
		t.Errorf("expected exactly 1 error for not-in-sops, got %d: %v", count, r.Errors)
	}
}

func TestSecrets_NilManifest(t *testing.T) {
	r := Secrets(context.Background(), nil, nil, nil)
	if !r.HasErrors() {
		t.Error("nil manifest should error")
	}
}

func TestSecrets_ResultErr(t *testing.T) {
	r := &Result{Errors: []string{"first", "second"}}
	err := r.Err()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") ||
		!strings.Contains(err.Error(), "2 error") {
		t.Errorf("error message missing pieces: %v", err)
	}
}

func TestSecrets_ResultErrEmpty(t *testing.T) {
	if err := (&Result{}).Err(); err != nil {
		t.Errorf("empty Result should yield nil error, got %v", err)
	}
}

// ---------- Vars ----------

func TestVars_Valid(t *testing.T) {
	r := Vars(baseManifest())
	if r.HasErrors() {
		t.Errorf("valid manifest should not error: %v", r.Errors)
	}
}

func TestVars_DelegatesToValidate(t *testing.T) {
	// Cause a failure validate.go catches — missing required app field.
	m := baseManifest()
	m.App.Location = ""

	r := Vars(m)
	if !r.HasErrors() {
		t.Fatal("expected error for missing location")
	}
	if !errorsContain(r.Errors, "location") {
		t.Errorf("errors = %v, want one about location", r.Errors)
	}
}

// ---------- helpers ----------

func errorsContain(list []string, substr string) bool {
	for _, s := range list {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func countContaining(list []string, substr string) int {
	n := 0
	for _, s := range list {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

func TestSecrets_RejectsInvalidName_InSOPS(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"DB": {SecretRef: "db-url"},
	}
	sops := map[string]string{
		"db-url":            "x",
		"kyc-bad_underscore": "y", // orphan-but-invalid: must error, not just warn
	}

	r := Secrets(context.Background(), m, sops, nil)
	if !r.HasErrors() {
		t.Fatal("expected error for invalid SOPS key name")
	}
	if !errorsContain(r.Errors, "kyc-bad_underscore") {
		t.Errorf("errors = %v, want one mentioning the bad name", r.Errors)
	}
	if !errorsContain(r.Errors, "kyc-bad-underscore") {
		t.Errorf("errors = %v, want a hyphenated suggestion", r.Errors)
	}
}

func TestSecrets_RejectsInvalidName_InManifestRef(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"DB": {SecretRef: "kyc-database_url"}, // bad name
	}
	// SOPS doesn't have it either, but the name-rule error should come
	// before (or alongside) the missing-from-SOPS error.
	r := Secrets(context.Background(), m, map[string]string{}, nil)
	if !errorsContain(r.Errors, "Azure Key Vault") {
		t.Errorf("errors = %v, want one mentioning the Azure name rule", r.Errors)
	}
}

func TestSecrets_NameCheckReportsEachOnce(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"DB": {SecretRef: "kyc-database_url"},
	}
	// Same bad name on both sides.
	sops := map[string]string{"kyc-database_url": "x"}

	r := Secrets(context.Background(), m, sops, nil)
	if c := countContaining(r.Errors, "kyc-database_url"); c != 1 {
		t.Errorf("expected 1 name-rule error for kyc-database_url, got %d (errors: %v)", c, r.Errors)
	}
}
