package hooks

// Event names one of the eleven core hook events. The strings are the exact
// event names Claude Code and Codex both use.
type Event string

// The eleven core events. Codex's Interrupt and Claude Code's extension
// events (Setup, Notification, CwdChanged, ...) are deliberately excluded.
const (
	EventSessionStart      Event = "SessionStart"
	EventSessionEnd        Event = "SessionEnd"
	EventUserPromptSubmit  Event = "UserPromptSubmit"
	EventPreToolUse        Event = "PreToolUse"
	EventPostToolUse       Event = "PostToolUse"
	EventPermissionRequest Event = "PermissionRequest"
	EventPreCompact        Event = "PreCompact"
	EventPostCompact       Event = "PostCompact"
	EventSubagentStart     Event = "SubagentStart"
	EventSubagentStop      Event = "SubagentStop"
	EventStop              Event = "Stop"
)

// Events returns the eleven core events in a fixed order. The result is a
// fresh slice on every call.
func Events() []Event {
	return []Event{
		EventSessionStart,
		EventSessionEnd,
		EventUserPromptSubmit,
		EventPreToolUse,
		EventPostToolUse,
		EventPermissionRequest,
		EventPreCompact,
		EventPostCompact,
		EventSubagentStart,
		EventSubagentStop,
		EventStop,
	}
}

// Valid reports whether e is exactly one of the eleven core events.
func (e Event) Valid() bool {
	switch e {
	case EventSessionStart, EventSessionEnd, EventUserPromptSubmit,
		EventPreToolUse, EventPostToolUse, EventPermissionRequest,
		EventPreCompact, EventPostCompact, EventSubagentStart,
		EventSubagentStop, EventStop:
		return true
	default:
		return false
	}
}

// UsesToolMatcher reports whether Hook.Matcher applies to e. Only the tool
// events match on tool_name; every other event fires unconditionally.
func (e Event) UsesToolMatcher() bool {
	switch e {
	case EventPreToolUse, EventPostToolUse, EventPermissionRequest:
		return true
	default:
		return false
	}
}
