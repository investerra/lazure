package lazurecfg

import (
	"strings"
	"testing"
)

func sharedFixture() map[string]*EnvValue {
	return map[string]*EnvValue{
		"DEBUG":        {Value: "0"},
		"LOG_LEVEL":    {Value: "info"},
		"DATABASE_URL": {SecretRef: "nexus-database-url"},
		"REDIS_URL":    {SecretRef: "nexus-redis-url"},
	}
}

func TestResolveEnv_Inherit(t *testing.T) {
	c := &Container{Name: "app"}
	got, err := ResolveEnv(sharedFixture(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("inherited len = %d, want 4", len(got))
	}
	if got["DEBUG"].Value != "0" {
		t.Errorf("DEBUG = %+v", got["DEBUG"])
	}
	if got["DATABASE_URL"].SecretRef != "nexus-database-url" {
		t.Errorf("DATABASE_URL = %+v", got["DATABASE_URL"])
	}
}

func TestResolveEnv_Replace(t *testing.T) {
	c := &Container{
		Name: "sidecar",
		Env: map[string]*EnvValue{
			"EXPORTER_PORT": {Value: "9100"},
			"LOG_LEVEL":     {Value: "warning"},
		},
	}
	got, err := ResolveEnv(sharedFixture(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("replace len = %d, want 2 (shared discarded)", len(got))
	}
	if got["EXPORTER_PORT"].Value != "9100" {
		t.Errorf("EXPORTER_PORT = %+v", got["EXPORTER_PORT"])
	}
	if got["LOG_LEVEL"].Value != "warning" {
		t.Errorf("LOG_LEVEL = %+v", got["LOG_LEVEL"])
	}
	if _, has := got["DEBUG"]; has {
		t.Errorf("DEBUG leaked from shared into replace mode")
	}
}

func TestResolveEnv_Merge_AddAndOverride(t *testing.T) {
	c := &Container{
		Name: "tasks",
		MergeEnv: map[string]*EnvValue{
			"LOG_LEVEL":    {Value: "debug"},             // override shared
			"WORKER_COUNT": {Value: "4"},                  // add new
		},
	}
	got, err := ResolveEnv(sharedFixture(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Errorf("merge len = %d, want 5", len(got))
	}
	if got["DEBUG"].Value != "0" {
		t.Errorf("DEBUG inherited incorrectly: %+v", got["DEBUG"])
	}
	if got["LOG_LEVEL"].Value != "debug" {
		t.Errorf("LOG_LEVEL override = %+v, want 'debug'", got["LOG_LEVEL"])
	}
	if got["WORKER_COUNT"].Value != "4" {
		t.Errorf("WORKER_COUNT addition = %+v", got["WORKER_COUNT"])
	}
}

func TestResolveEnv_Merge_NullDeletes(t *testing.T) {
	c := &Container{
		Name: "init",
		MergeEnv: map[string]*EnvValue{
			"REDIS_URL":      nil,                     // null delete
			"RUN_MIGRATIONS": {Value: "true"},
		},
	}
	got, err := ResolveEnv(sharedFixture(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["REDIS_URL"]; has {
		t.Errorf("REDIS_URL should be deleted via null")
	}
	if got["RUN_MIGRATIONS"].Value != "true" {
		t.Errorf("RUN_MIGRATIONS = %+v", got["RUN_MIGRATIONS"])
	}
	// Other shared keys still present.
	if got["DATABASE_URL"].SecretRef != "nexus-database-url" {
		t.Errorf("DATABASE_URL should have been preserved: %+v", got["DATABASE_URL"])
	}
}

func TestResolveEnv_Merge_NullOnMissingKeyIsNoOp(t *testing.T) {
	c := &Container{
		Name: "app",
		MergeEnv: map[string]*EnvValue{
			"NOT_IN_SHARED": nil,
		},
	}
	got, err := ResolveEnv(sharedFixture(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("len = %d, want 4 (deletion of missing key is no-op)", len(got))
	}
	if _, has := got["NOT_IN_SHARED"]; has {
		t.Errorf("NOT_IN_SHARED should not be present")
	}
}

func TestResolveEnv_BothSetIsError(t *testing.T) {
	c := &Container{
		Name:     "bad",
		Env:      map[string]*EnvValue{"A": {Value: "1"}},
		MergeEnv: map[string]*EnvValue{"B": {Value: "2"}},
	}
	_, err := ResolveEnv(sharedFixture(), c)
	if err == nil {
		t.Fatal("expected error for both env and merge_env set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want 'mutually exclusive'", err.Error())
	}
}

func TestResolveEnv_NilContainer(t *testing.T) {
	_, err := ResolveEnv(sharedFixture(), nil)
	if err == nil {
		t.Fatal("expected error for nil container")
	}
}

func TestResolveEnv_EmptyEnvReplacesWithEmpty(t *testing.T) {
	// Explicit empty env: {} = "this container has NO env vars"
	c := &Container{
		Name: "barebones",
		Env:  map[string]*EnvValue{},
	}
	got, err := ResolveEnv(sharedFixture(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty-env-replace should yield empty map, got %d entries", len(got))
	}
}

func TestResolveEnv_EmptyMergeEnvInherits(t *testing.T) {
	// merge_env: {} with no entries = inherit shared, no changes
	c := &Container{
		Name:     "app",
		MergeEnv: map[string]*EnvValue{},
	}
	got, err := ResolveEnv(sharedFixture(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("empty-merge_env should inherit all shared, got %d", len(got))
	}
}

func TestResolveEnv_NoSharedPlusMerge(t *testing.T) {
	c := &Container{
		Name: "app",
		MergeEnv: map[string]*EnvValue{
			"FOO": {Value: "bar"},
		},
	}
	got, err := ResolveEnv(nil, c)
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"].Value != "bar" {
		t.Errorf("FOO missing in result: %+v", got)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 entry, got %d", len(got))
	}
}

// TestResolveEnv_ReturnedMapIsIndependent verifies the caller can mutate the
// returned map without affecting the source shared / container maps.
func TestResolveEnv_ReturnedMapIsIndependent(t *testing.T) {
	shared := sharedFixture()
	c := &Container{Name: "app"}
	got, err := ResolveEnv(shared, c)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the returned map.
	delete(got, "DEBUG")
	got["NEW"] = &EnvValue{Value: "x"}
	// Shared must be unchanged.
	if _, has := shared["DEBUG"]; !has {
		t.Errorf("mutation of result leaked into shared")
	}
	if _, has := shared["NEW"]; has {
		t.Errorf("mutation of result leaked into shared")
	}
}
