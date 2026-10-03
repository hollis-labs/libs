package pluginhooks

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
)

// CatalogDocument is introspection data, never an executable catalog. Hosts
// must attach compiled validators before installing declarations in a Registry.
// CatalogVersion is independent of the Go module and subprocess protocol versions.
type CatalogDocument struct {
	CatalogVersion string               `json:"catalog_version"`
	Definitions    []DefinitionDocument `json:"definitions"`
}

// DefinitionDocument carries inline schemas and explicit policy, without Go
// functions. Durations are milliseconds (fractional for sub-millisecond limits).
type DefinitionDocument struct {
	Name             string              `json:"name"`
	OwnerNamespace   string              `json:"owner_namespace,omitempty"`
	Kind             Kind                `json:"kind"`
	Mode             Mode                `json:"mode"`
	InputSchema      json.RawMessage     `json:"input_schema"`
	OutputSchema     json.RawMessage     `json:"output_schema,omitempty"`
	MutablePaths     []string            `json:"mutable_paths"`
	Since            string              `json:"since"`
	Deprecated       *Deprecation        `json:"deprecated,omitempty"`
	RemoteOK         *bool               `json:"remote_ok"`
	BudgetMS         float64             `json:"budget_ms"`
	HandlerTimeoutMS float64             `json:"handler_timeout_ms"`
	OnErrorDefault   ErrorPolicy         `json:"on_error_default"`
	AllowedOnError   []ErrorPolicy       `json:"allowed_on_error"`
	MaxPayloadBytes  int                 `json:"max_payload_bytes"`
	MaxHandlers      int                 `json:"max_handlers"`
	MaxParallelism   int                 `json:"max_parallelism"`
	Views            map[string][]string `json:"views"`
	RequiredView     string              `json:"required_view,omitempty"`
	SchemaDigest     string              `json:"schema_digest"`
}

func definitionDocument(d Definition) DefinitionDocument {
	d = copyDefinition(d)
	if d.MutablePaths == nil {
		d.MutablePaths = []string{}
	}
	if d.Views == nil {
		d.Views = map[string][]string{}
	}
	return DefinitionDocument{
		Name: d.Name, OwnerNamespace: d.OwnerNamespace, Kind: d.Kind, Mode: d.Mode,
		InputSchema: d.InputSchema, OutputSchema: d.OutputSchema, MutablePaths: d.MutablePaths,
		Since: d.Since, Deprecated: d.Deprecated, RemoteOK: d.RemoteOK,
		BudgetMS:         float64(d.Budget) / float64(time.Millisecond),
		HandlerTimeoutMS: float64(d.HandlerTimeout) / float64(time.Millisecond),
		OnErrorDefault:   d.OnErrorDefault, AllowedOnError: d.AllowedOnError,
		MaxPayloadBytes: d.MaxPayloadBytes, MaxHandlers: d.MaxHandlers, MaxParallelism: d.MaxParallelism,
		Views: d.Views, RequiredView: d.RequiredView, SchemaDigest: d.SchemaDigest,
	}
}

// Document returns a detached, name-sorted introspection snapshot.
func (c Catalog) Document() CatalogDocument {
	definitions := slices.Clone(c.Definitions)
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	doc := CatalogDocument{CatalogVersion: c.Version, Definitions: make([]DefinitionDocument, 0, len(definitions))}
	for _, d := range definitions {
		doc.Definitions = append(doc.Definitions, definitionDocument(d))
	}
	return doc
}

// MarshalJSON serializes only declarations and policy, never validator functions.
func (c Catalog) MarshalJSON() ([]byte, error) { return json.Marshal(c.Document()) }

// MarshalJSON serializes a definition as introspection data.
func (d Definition) MarshalJSON() ([]byte, error) { return json.Marshal(definitionDocument(d)) }

// WriteMarkdown generates reference documentation from the same introspection
// document as HTTP discovery. Schema JSON is indented, not interpolated as markup.
// A writer failure is returned to the caller.
func (c Catalog) WriteMarkdown(w io.Writer) error {
	doc := c.Document()
	var out strings.Builder
	fmt.Fprintf(&out, "# Hook catalog %s\n\n", markdownText(doc.CatalogVersion))
	out.WriteString("Declarations and policy only. Discovery grants no registration authority.\n\n")
	for _, d := range doc.Definitions {
		fmt.Fprintf(&out, "## %s\n\n", markdownText(d.Name))
		b, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return fmt.Errorf("document %s: %w", d.Name, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			fmt.Fprintf(&out, "    %s\n", line)
		}
		out.WriteString("\n")
	}
	_, err := io.WriteString(w, strings.TrimRight(out.String(), "\n")+"\n")
	return err
}
func markdownText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\r", " ", "\n", " ", "<", "&lt;", ">", "&gt;", "&", "&amp;", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`", "#", "\\#", "!", "\\!").Replace(s)
}
