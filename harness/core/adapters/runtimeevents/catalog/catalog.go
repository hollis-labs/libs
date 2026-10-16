// Package catalog is the registry of event kinds and their payload schemas
// for the runtimeevents envelope (event stream contract C2).
//
// catalog.json is the single source. For each kind it records a category
// (the class.category value producers set), a status (stable, new or
// deprecated), an optional payload schema id (id@version, the
// class.payload_schema value) and, for a deprecated kind, its successor.
// The payload schemas in schemas/ are minimums: producers may add fields
// and consumers must tolerate unknown fields and unknown kinds.
//
// kinds_gen.go (Go) and kinds.ts (TypeScript) hold one constant per kind,
// generated from catalog.json. After editing catalog.json, regenerate them
// with:
//
//	go test ./adapters/runtimeevents/catalog -run TestGenerated -update
//
// mapping.md maps the kinds each producer emits today onto this catalog.
// selfreport.md, with [EffectiveProgressDetail] and [ToolSchema], is the
// progress_detail setting and the agent self-report call contract.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed catalog.json
var catalogJSON []byte

//go:embed schemas/*.schema.json
var schemaFS embed.FS

// Status values for [Entry.Status].
const (
	StatusStable     = "stable"
	StatusNew        = "new"
	StatusDeprecated = "deprecated"
)

// Entry is one registered kind.
type Entry struct {
	Kind          string `json:"kind"`
	Category      string `json:"category"`
	PayloadSchema string `json:"payload_schema,omitempty"`
	Status        string `json:"status"`
	Successor     string `json:"successor,omitempty"`
	Note          string `json:"note,omitempty"`
}

// Catalog is the decoded catalog.json.
type Catalog struct {
	Version    int      `json:"version"`
	Naming     string   `json:"naming"`
	Categories []string `json:"categories"`
	Kinds      []Entry  `json:"kinds"`
}

var (
	loadOnce sync.Once
	loaded   Catalog
	byKind   map[string]Entry
	loadErr  error
)

func load() {
	if loadErr = json.Unmarshal(catalogJSON, &loaded); loadErr != nil {
		return
	}
	byKind = make(map[string]Entry, len(loaded.Kinds))
	for _, e := range loaded.Kinds {
		byKind[e.Kind] = e
	}
}

// Load returns the registry. It fails only if the embedded catalog.json is
// malformed, which the package tests rule out.
func Load() (Catalog, error) {
	loadOnce.Do(load)
	return loaded, loadErr
}

// Lookup returns the entry for kind. An unknown kind is not an error for
// consumers: they must treat it as opaque.
func Lookup(kind string) (Entry, bool) {
	loadOnce.Do(load)
	e, ok := byKind[kind]
	return e, ok
}

// PayloadSchema returns the JSON Schema document registered under id
// (for example "urn:hollis-labs:events:state_changed@1").
func PayloadSchema(id string) ([]byte, error) {
	name, ok := strings.CutPrefix(id, "urn:hollis-labs:events:")
	if !ok || name == "" {
		return nil, fmt.Errorf("catalog: unknown payload schema %q", id)
	}
	b, err := schemaFS.ReadFile("schemas/" + name + ".schema.json")
	if err != nil {
		return nil, fmt.Errorf("catalog: unknown payload schema %q", id)
	}
	return b, nil
}
