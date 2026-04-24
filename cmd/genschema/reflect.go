package main

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
)

// generateFor produces the JSON Schema for the given target's type.
// Split out from generate() so test code can pass a smaller type for
// faster / more focused assertions. invopop walks json: tags and
// honors JSONSchema() methods on types (lazurecfg.EnvValue uses this
// to describe its string | {secret: X} polymorphism).
func generateFor(target any) ([]byte, error) {
	r := &jsonschema.Reflector{
		// Inline defs rather than $ref so editors without robust
		// JSON-pointer resolution still show the full shape.
		ExpandedStruct: false,
		DoNotReference: false,
	}
	s := r.Reflect(target)
	return json.MarshalIndent(s, "", "  ")
}
