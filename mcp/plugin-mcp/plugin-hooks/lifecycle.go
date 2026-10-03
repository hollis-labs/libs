package pluginhooks

// LifecycleMapping describes an explicit host adapter from a native event name
// to a catalog declaration. This table does not install aliases or translate
// payloads; hosts own native input/output validation and permission behavior.
type LifecycleMapping struct {
	NativeEvent string
	Hook        string
	Contract    string
}

// AgentLifecycleMappings returns a fresh table for the eleven go-hooks events.
// In particular, Stop means turn.stopping, not session.end; PreCompact observes
// context.pre_compact and never becomes a mode-change or veto gate.
func AgentLifecycleMappings() []LifecycleMapping {
	return []LifecycleMapping{
		{"SessionStart", "session.start", "Observe boot; host collects bounded additionalContext/systemMessage"},
		{"SessionEnd", "session.end", "Observe actual session end; systemMessage is log only"},
		{"UserPromptSubmit", "message.sending", "Ordered gate; deny vetoes; context/reason are host return data"},
		{"PreToolUse", "tool.executing", "Gate; deny vetoes; ask parks at host approval; validated updatedInput applies through tool.input after approval using one cached native result"},
		{"PostToolUse", "tool.complete", "Observe completion; host collects bounded context/systemMessage"},
		{"PermissionRequest", "permission.requesting", "Ordered gate; host handles allow/deny/ask; ask never auto-allows"},
		{"PreCompact", "context.pre_compact", "Non-veto observation before compaction; host collects bounded context/systemMessage"},
		{"PostCompact", "context.compacted", "Non-veto observation after compaction; collect context for next turn"},
		{"SubagentStart", "subagent.start", "Observe boot; host collects bounded context"},
		{"SubagentStop", "subagent.stopping", "Ordered gate; continue=false defers stop; preserve recursion marker"},
		{"Stop", "turn.stopping", "Ordered gate; continue=false defers the current turn, not session end"},
	}
}
