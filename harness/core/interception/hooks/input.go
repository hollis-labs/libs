package hooks

// CommonInput is embedded at the top level of every event's *Input type.
// Claude Code and Codex both write a flat JSON object to a command hook's
// stdin (no nested payload key); struct embedding gives the same flat shape
// on encode and decode.
type CommonInput struct {
	Event     Event  `json:"hook_event_name"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	// TranscriptPath and PermissionMode are sent by both Claude Code and
	// Codex; both are optional here.
	TranscriptPath string `json:"transcript_path,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
}

// SessionStartInput is the SessionStart payload.
type SessionStartInput struct {
	CommonInput
	// Source is one of "startup", "resume", "clear", "compact" (Codex) or
	// additionally "fork" (Claude Code). The set is not closed here.
	Source string `json:"source,omitempty"`
}

// SessionEndInput is the SessionEnd payload.
type SessionEndInput struct {
	CommonInput
	// Reason is host-defined; Claude Code documents "clear", "resume",
	// "logout", "prompt_input_exit" and "other".
	Reason string `json:"reason,omitempty"`
}

// UserPromptSubmitInput is the UserPromptSubmit payload.
type UserPromptSubmitInput struct {
	CommonInput
	Prompt string `json:"prompt"`
}

// PreToolUseInput is the PreToolUse payload.
type PreToolUseInput struct {
	CommonInput
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
}

// PostToolUseInput is the PostToolUse payload.
type PostToolUseInput struct {
	CommonInput
	ToolName   string         `json:"tool_name"`
	ToolInput  map[string]any `json:"tool_input"`
	ToolResult any            `json:"tool_result"`
	ToolUseID  string         `json:"tool_use_id,omitempty"`
}

// PermissionRequestInput is the PermissionRequest payload.
type PermissionRequestInput struct {
	CommonInput
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Reason    string         `json:"reason,omitempty"`
}

// PreCompactInput is the PreCompact payload.
type PreCompactInput struct {
	CommonInput
	// Trigger is "manual" or "auto".
	Trigger string `json:"trigger,omitempty"`
}

// PostCompactInput is the PostCompact payload.
type PostCompactInput struct {
	CommonInput
	// Trigger is "manual" or "auto".
	Trigger string `json:"trigger,omitempty"`
}

// SubagentStartInput is the SubagentStart payload.
type SubagentStartInput struct {
	CommonInput
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type,omitempty"`
}

// SubagentStopInput is the SubagentStop payload.
type SubagentStopInput struct {
	CommonInput
	AgentID              string `json:"agent_id"`
	AgentType            string `json:"agent_type,omitempty"`
	StopHookActive       bool   `json:"stop_hook_active,omitempty"`
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
}

// StopInput is the Stop payload.
type StopInput struct {
	CommonInput
	StopHookActive       bool   `json:"stop_hook_active,omitempty"`
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
}
