package schema

import (
	"encoding/json"
	"testing"
)

// TestEmbed_NonEmpty guards against a missing go:embed pattern (which
// would silently leave JSON == nil).
func TestEmbed_NonEmpty(t *testing.T) {
	if len(JSON) == 0 {
		t.Fatal("embedded schema is empty — run: go run ./cmd/genschema internal/schema/schema.json")
	}
}

// TestEmbed_ValidJSON catches a regression where the generator emits
// something that parses in the generator's own test but is corrupted
// on the way to embed (e.g. a trailing null byte from a previous
// write, or a partial write).
func TestEmbed_ValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(JSON, &v); err != nil {
		t.Fatalf("embedded schema is not valid JSON: %v", err)
	}
	if _, ok := v["$defs"]; !ok {
		t.Error("embedded schema missing $defs key — looks truncated")
	}
}
