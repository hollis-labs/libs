// Command manifest emits a generated plugin.yaml declaration on stdout.
// Run: go run ./examples/manifest > plugin.yaml
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hollis-labs/plugin-sdk/manifest"
)

func main() {
	m := manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion,
		ID:            "example.notes", Name: "Notes", Version: "0.1.0", License: "MIT",
		Description: "Example declaration for a notes plugin",
		Protocol:    manifest.RequiredProtocol, Runtime: manifest.Runtime,
		Server:   manifest.Server{Runtime: "node", Entry: "bin/notes.js", Engines: map[string]manifest.HostRange{"node": {Min: "22.0.0"}}},
		Artifact: exampleArtifact(),
		Hooks:    []manifest.Hook{{Name: "session.end", Once: new(false), View: new("summary"), Mode: "sequential", Timeout: 1000, OnError: "open"}},
		Hosts:    map[string]manifest.HostRange{"nanite": {Min: "0.1.0"}},
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

// An author build hashes the actual staged bytes. This example uses an empty
// JavaScript worker only to demonstrate the manifest API, not a runnable plugin.
func exampleArtifact() manifest.Artifact {
	files := []manifest.ArtifactFile{{Path: "bin/notes.js", SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}}
	digest, err := manifest.TreeDigest(files)
	if err != nil {
		panic(err)
	}
	return manifest.Artifact{Files: files, TreeSHA256: digest}
}
