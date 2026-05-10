package plugin

// EnvelopeOut is an envelope emitted by a plugin as the result of a command,
// event handler, or MCP tool call. Envelopes are validated by the host
// against the plugin's declared envelope types and their JSON Schemas
// before being surfaced to the UI.
//
// Envelope-type registration is declarative: plugins declare envelope
// types in plugin.yaml and return EnvelopeOut values from capability
// handlers. Plugins do not register envelope types programmatically.
type EnvelopeOut struct {
	// Type is the envelope type identifier, e.g. "oembed.card" or
	// "support-ticket.created". Must match one of the types declared in
	// the plugin's plugin.yaml registers.envelopes section.
	Type string `json:"type"`

	// Data is the envelope payload. Validated against the schema
	// registered for Type.
	Data map[string]interface{} `json:"data"`

	// SessionID routes the envelope to a specific chat session. Empty
	// means broadcast to all listeners.
	SessionID string `json:"session_id,omitempty"`
}

// MessageOut is a chat message produced by a plugin. Typically used by
// command handlers that want to inject a message into the session rather
// than emit a rendered envelope.
type MessageOut struct {
	// SessionID is the chat session to post into.
	SessionID string `json:"session_id"`

	// Role is the message role: "assistant", "system", or "tool".
	Role string `json:"role"`

	// Content is the message body (markdown).
	Content string `json:"content"`

	// Envelopes are optional envelopes to attach alongside the message.
	Envelopes []EnvelopeOut `json:"envelopes,omitempty"`
}
