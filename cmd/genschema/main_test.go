package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/investerra/lazure/internal/lazurecfg"
)

// TestGenerate_ShapeCheck decodes the generated JSON and verifies the
// manifest schema has the major lazurecfg blocks as $defs. Full
// structural fidelity is verified by the CI schema-drift check
// (lazure-mrz) comparing to the committed internal/schema/schema.json
// — this test just guards that the generator runs clean and produces
// a recognizable shape.
func TestGenerate_ShapeCheck(t *testing.T) {
	out, err := generate()
	if err != nil {
		t.Fatal(err)
	}

	var root struct {
		Ref  string                 `json:"$ref"`
		Defs map[string]any         `json:"$defs"`
	}
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("generated schema is not valid JSON: %v\n%s", err, out)
	}

	if root.Ref == "" || !strings.HasSuffix(root.Ref, "/Manifest") {
		t.Errorf("$ref should point at Manifest, got %q", root.Ref)
	}

	// Core blocks the manifest must describe. Missing any of these is a
	// regression — either the type was renamed without regenerating, or
	// invopop reflection stopped seeing it (e.g. a tagged field got
	// hidden behind json:"-").
	wantDefs := []string{
		"Manifest", "App", "Ingress", "Scale", "ScaleRule", "Container",
		"Probe", "EnvValue", "Registry", "Volume", "VolumeMount",
	}
	for _, name := range wantDefs {
		if _, ok := root.Defs[name]; !ok {
			t.Errorf("$defs missing %q", name)
		}
	}
}

// TestGenerateFor_SmallType proves the helper works with an arbitrary
// struct, useful when future changes want to test a subset of the
// schema without generating the full Manifest output.
func TestGenerateFor_SmallType(t *testing.T) {
	type probe struct {
		Path string `json:"path"`
		Port int    `json:"port"`
	}
	out, err := generateFor(&probe{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"path"`) {
		t.Errorf("expected path field in output:\n%s", out)
	}
}

// TestGenerate_EnvValueHasOneOf covers the JSONSchema() hook on
// lazurecfg.EnvValue — invopop's default reflection would emit a
// plain object with value/secret properties, but the hook makes it
// emit the polymorphic string | {secret: X} oneOf shape that matches
// our UnmarshalJSON behavior.
func TestGenerate_EnvValueHasOneOf(t *testing.T) {
	out, err := generateFor(&lazurecfg.EnvValue{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"oneOf"`) {
		t.Errorf("expected oneOf for EnvValue:\n%s", s)
	}
	if !strings.Contains(s, `"secret"`) {
		t.Errorf("expected secret shape in EnvValue schema:\n%s", s)
	}
}
