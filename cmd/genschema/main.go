// genschema reflects on lazurecfg.Manifest and writes a JSON Schema
// for deploy.yml. This is a DEV/CI tool — the runtime lazure binary
// doesn't import invopop/jsonschema; it only ships the bytes this
// tool produces (via go:embed in internal/schema/).
//
// Usage:
//
//	go run ./cmd/genschema                          # to stdout
//	go run ./cmd/genschema internal/schema/schema.json  # to file (typical dev flow)
//
// CI drift check (task lazure-mrz) runs the file form and fails on
// `git diff --exit-code internal/schema/`.
package main

import (
	"fmt"
	"os"

	"github.com/investerra/lazure/internal/lazurecfg"
)

func main() {
	out, err := generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
	out = append(out, '\n')

	if len(os.Args) >= 2 {
		path := os.Args[1]
		if err := os.WriteFile(path, out, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "genschema: write:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %d bytes to %s\n", len(out), path)
		return
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintln(os.Stderr, "genschema: stdout:", err)
		os.Exit(1)
	}
}

// generate returns the marshaled JSON Schema bytes for a
// lazurecfg.Manifest. Separated from main() so tests can exercise it
// without needing to capture stdout.
func generate() ([]byte, error) {
	return generateFor(&lazurecfg.Manifest{})
}
