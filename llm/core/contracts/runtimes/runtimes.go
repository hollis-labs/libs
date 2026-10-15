package runtimes

// ID is the canonical id of an agent CLI runtime.
type ID string

// The runtimes. Gemini CLI is not one: Antigravity replaced it.
const (
	Claude      ID = "claude"
	Codex       ID = "codex"
	OpenCode    ID = "opencode"
	Copilot     ID = "copilot"
	Pi          ID = "pi"
	Antigravity ID = "antigravity"
)

// Valid reports whether id is one of the six runtimes.
func (id ID) Valid() bool {
	switch id {
	case Claude, Codex, OpenCode, Copilot, Pi, Antigravity:
		return true
	default:
		return false
	}
}

// IDs returns every runtime, in canonical order.
func IDs() []ID {
	return []ID{Claude, Codex, OpenCode, Copilot, Pi, Antigravity}
}

// Mode is how a runtime is driven: the lifecycle and wire shape of the process
// a launcher starts. It is a transport, not a policy; permission posture and
// run policy are separate axes.
type Mode string

// The modes.
const (
	// ModeStreamingStdio is one long-lived process speaking NDJSON over
	// stdin/stdout.
	ModeStreamingStdio Mode = "streaming-stdio"
	// ModeSubprocessPerTurn spawns a fresh process for each turn; it exits
	// when the turn ends.
	ModeSubprocessPerTurn Mode = "subprocess-per-turn"
	// ModeJSONRPCStdio is one long-lived process speaking JSON-RPC 2.0 over
	// stdin/stdout (Codex app-server).
	ModeJSONRPCStdio Mode = "jsonrpc-stdio"
	// ModeHTTPSSE is one long-lived process serving an HTTP API with
	// server-sent events (OpenCode serve).
	ModeHTTPSSE Mode = "http-sse"
	// ModePTY is one long-lived process behind a pseudo-terminal: the
	// interactive TUI.
	ModePTY Mode = "pty"
	// ModeACPStdio speaks the Agent Client Protocol over stdin/stdout.
	ModeACPStdio Mode = "acp-stdio"
	// ModeACPTCP speaks the Agent Client Protocol over a TCP socket.
	ModeACPTCP Mode = "acp-tcp"
)

// Valid reports whether m is one of the seven modes.
func (m Mode) Valid() bool {
	switch m {
	case ModeStreamingStdio, ModeSubprocessPerTurn, ModeJSONRPCStdio, ModeHTTPSSE,
		ModePTY, ModeACPStdio, ModeACPTCP:
		return true
	default:
		return false
	}
}

// ACP reports whether m speaks the Agent Client Protocol.
func (m Mode) ACP() bool {
	return m == ModeACPStdio || m == ModeACPTCP
}

// Modes returns every mode, in canonical order.
func Modes() []Mode {
	return []Mode{ModeStreamingStdio, ModeSubprocessPerTurn, ModeJSONRPCStdio, ModeHTTPSSE,
		ModePTY, ModeACPStdio, ModeACPTCP}
}

// Capability is something a runtime, driven in a given mode, does that a
// launcher may rely on. A runtime declares capabilities per mode; an
// undeclared capability is absent, never assumed.
//
// These are runtime facts, distinct from the agent-facing vocabulary in package
// capabilities: a host can truthfully list capabilities.Resume as supported
// only when the runtime it launches declares [CapResume] in that mode.
type Capability string

// The capabilities.
const (
	// CapResume: a later launch can continue an earlier conversation by its
	// session id.
	CapResume Capability = "resume"
	// CapResumeKeepsID: resuming reports the same session id it was given, so
	// a different id means the requested session was not continued.
	CapResumeKeepsID Capability = "resume-keeps-id"
	// CapTypedEvents: the runtime's output parses into typed per-step events
	// (deltas, tool use, usage) rather than an opaque terminal stream.
	CapTypedEvents Capability = "typed-events"
	// CapPreflight: a cheap check before launch fails with a typed error
	// instead of letting the runtime hit the failure itself (for example an
	// interactive login).
	CapPreflight Capability = "preflight"
	// CapAuthClassifier: a failed turn can be classified as "not
	// authenticated".
	CapAuthClassifier Capability = "auth-classifier"
	// CapSessionLostClassifier: a resume that names a session the runtime no
	// longer has is reported as session-lost, not as a generic failure or a
	// silent new conversation.
	CapSessionLostClassifier Capability = "session-lost-classifier"
	// CapApprovals: the runtime asks the host to approve actions mid-turn over
	// its own protocol.
	CapApprovals Capability = "approvals"
)

// Valid reports whether c is one of the defined capabilities.
func (c Capability) Valid() bool {
	switch c {
	case CapResume, CapResumeKeepsID, CapTypedEvents, CapPreflight,
		CapAuthClassifier, CapSessionLostClassifier, CapApprovals:
		return true
	default:
		return false
	}
}

// Capabilities returns every capability, in canonical order.
func Capabilities() []Capability {
	return []Capability{CapResume, CapResumeKeepsID, CapTypedEvents, CapPreflight,
		CapAuthClassifier, CapSessionLostClassifier, CapApprovals}
}
