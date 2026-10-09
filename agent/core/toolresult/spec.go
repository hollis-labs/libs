package toolresult

import "fmt"

// ToolSpec is an agent-facing tool definition as plain data. Map it onto your
// LLM library's tool type; this module imports none.
type ToolSpec struct {
	// Name is the tool name the model calls.
	Name string
	// Description is the prompt text describing the tool.
	Description string
	// InputSchema is the JSON Schema of the arguments. It is a fresh map on
	// every call, so callers may modify it.
	InputSchema map[string]any
}

// FetchSpec returns the definition of the fetch tool, whose calls the host
// routes to [Cache.HandleFetch]. The text is Nanite's, parameterized only by
// the configured pointer scheme, so its prompt surface is unchanged.
func (c *Cache) FetchSpec() ToolSpec {
	return ToolSpec{
		Name: c.cfg.FetchName,
		Description: "Read a page of a cached result from this chat session. Use the bare ID from a " + c.cfg.Scheme + ":// pointer when a preview is incomplete. " +
			"Omit json_pointer to read the original response; use an RFC 6901 pointer such as /data/comments/3 or /stdout to select a JSON value. Strings are returned as decoded text. " +
			"Offsets address UTF-8 bytes in the selected value, not the preview. Returns actual start/end offsets, total bytes, has_more and next_offset. " +
			"Follow next_offset with the same id and json_pointer until you have the evidence needed. The default and maximum page size use the current model's result budget. " +
			"IDs are session-scoped cache IDs, not file paths or permanent resource identifiers.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": fmt.Sprintf("The cached result ULID, taken verbatim from a `%s://<ULID>` footer in this session. NOT a file path, NOT a cache:// URI — a bare ULID string only.", c.cfg.Scheme),
				},
				"json_pointer": map[string]any{
					"type": "string", "description": "Optional RFC 6901 pointer into the original JSON, e.g. /stdout or /data/comments/3/content. Empty/omitted reads the original result. Escape ~ as ~0 and / as ~1 within a key.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Byte offset to start reading from (default: 0). Use to page through large results.",
				},
				"length": map[string]any{
					"type":        "integer",
					"description": "Requested bytes. Defaults to, and is capped by, the current model-aware result budget.",
				},
			},
			"required": []any{"id"},
			// additionalProperties: false is required for strict-mode compatibility.
			"additionalProperties": false,
		},
	}
}

// SearchSpec returns the definition of the search tool, whose calls the host
// routes to [Cache.HandleSearch]. See [Cache.FetchSpec].
func (c *Cache) SearchSpec() ToolSpec {
	return ToolSpec{
		Name: c.cfg.SearchName,
		Description: "Search cached result text for RE2 regex matches in this session. Use the bare ID from a " + c.cfg.Scheme + ":// pointer. " +
			"Optional json_pointer selects a JSON value, decoding strings such as /stdout; otherwise search the original response. " +
			"Returns matching lines with bounded surrounding context and byte coordinates usable by " + c.cfg.FetchName + " with the same json_pointer. " +
			"has_more and next_offset report whether further matching lines were omitted. Continue with offset=next_offset. " +
			"A preview or limited search is not evidence that the whole result has been read. Use filesystem tools for source files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": fmt.Sprintf("The cached result ULID, taken verbatim from a `%s://<ULID>` footer in this session. NOT a file path — a bare ULID string only.", c.cfg.Scheme),
				},
				"pattern": map[string]any{
					"type":        "string",
					"description": "RE2 regex pattern to search for. Case-sensitive by default. Use (?i) prefix for case-insensitive.",
				},
				"json_pointer": map[string]any{
					"type": "string", "description": "Optional RFC 6901 pointer selecting the same value as " + c.cfg.FetchName + ". Strings are decoded before searching.",
				},
				"offset": map[string]any{
					"type": "integer", "description": "Non-negative byte offset in the selected value; use next_offset to continue a limited search.",
				},
				"max_matches": map[string]any{
					"type":        "integer",
					"description": "Maximum matching lines (default 20, capped at 100). Context also obeys the current model-aware result budget; has_more reports further matches.",
				},
			},
			"required": []any{"id", "pattern"},
			// additionalProperties: false is required for strict-mode compatibility.
			"additionalProperties": false,
		},
	}
}
