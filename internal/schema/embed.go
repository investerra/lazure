// Package schema holds the JSON Schema for deploy.yml as a byte slice
// embedded into the lazure binary at build time. Consumers read the
// bytes and write them to disk (e.g. as deploy.schema.json next to
// deploy.yml) so editors with a yaml-language-server modeline pick up
// autocomplete + validation.
//
// Regenerate via:
//
//	go run ./cmd/genschema internal/schema/schema.json
package schema

import _ "embed"

// JSON is the canonical manifest schema bytes, generated from
// lazurecfg.Manifest. Safe to read directly; do not mutate.
//
//go:embed schema.json
var JSON []byte
