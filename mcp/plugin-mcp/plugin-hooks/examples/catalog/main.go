// Command catalog generates a proposed host catalog sample as Markdown.
// These illustrative envelope schemas need host-specific refinement and compiled
// validators before installation. This command does not migrate existing hooks.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
)

func main() {
	no := false
	catalog := hooks.Catalog{Version: "1"}
	// The source vocabulary is Nanite's existing filter constants; dotted names
	// below are proposals, not supported runtime declarations or legacy aliases.
	for _, sample := range []struct{ name, valueType string }{
		{"context.window", "array"},
		{"assistant.response", "string"},
		{"envelope.data", "object"},
		{"system.prompt", "string"},
		{"user.message", "string"},
		{"tool.result", "string"},
		{"tool.selection", "array"},
		{"reflex.state", "object"},
		{"reflex.action", "object"},
	} {
		schema := json.RawMessage(fmt.Sprintf(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["value"],"additionalProperties":false,"properties":{"value":{"type":%q}}}`, sample.valueType))
		catalog.Definitions = append(catalog.Definitions, hooks.Definition{
			Name: sample.name, Kind: hooks.Filter, Mode: hooks.Waterfall,
			InputSchema: schema, OutputSchema: schema, MutablePaths: []string{"/value"},
			Since: "proposed", RemoteOK: &no, Budget: 5 * time.Second, HandlerTimeout: time.Second,
			OnErrorDefault: hooks.Open, AllowedOnError: []hooks.ErrorPolicy{hooks.Open},
			MaxPayloadBytes: 1 << 20, MaxHandlers: 64, MaxParallelism: 1,
			SchemaDigest: "illustrative-not-for-registration",
		})
	}
	if err := catalog.WriteMarkdown(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
