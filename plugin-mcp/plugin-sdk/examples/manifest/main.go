// Command manifest emits a generated plugin.yaml declaration on stdout.
// Run: go run ./examples/manifest > plugin.yaml
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func main() {
	m := manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion,
		ID:            "example.notes", Name: "Notes", Version: "0.1.0", License: "MIT",
		Description: "Example declaration for a notes plugin",
		Protocol:    subprocess.ProtocolVersion, Runtime: manifest.Runtime,
		Entrypoint: manifest.Entrypoint{Command: "bin/notes"},
		Hosts:      map[string]manifest.HostRange{"nanite": {Min: "0.1.0"}},
		Tools: []manifest.Tool{{
			Name: "notes_list", Description: "List saved notes", Effect: "read",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}},
		Nanite: json.RawMessage(`{}`),
	}
	if err := manifest.Encode(os.Stdout, m); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
