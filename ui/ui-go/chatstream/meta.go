package chatstream

import "encoding/json"

// Conventions for information the Event struct has no field of its own for.
// They live in Event.Meta (on part.start) and in activity kinds, so decoders and
// encoders in different packages agree without a core change. The conformance
// kit's Validate enforces the two that a consumer cannot render without.
const (
	// MetaName is the tool name on a tool_call part.start. Required: a tool call
	// with no name cannot be rendered. It is also the name of a data part
	// ("weather" becomes AI SDK's data-weather).
	MetaName = "name"
	// MetaCallID is, on a tool_result part.start, the part id of the tool_call
	// part it answers. Required: a result must say which call it is for.
	MetaCallID = "call_id"
	// MetaIsError is, on a tool_result part.start, true when the tool failed.
	MetaIsError = "is_error"
	// MetaDetail is a tool_call's short human label (a command, a path).
	MetaDetail = "detail"
	// MetaPhase is a text part's narrative role: "narration" or "final".
	MetaPhase = "phase"
	// MetaURL, MetaMediaType, MetaTitle, MetaFilename and MetaSourceID describe
	// source and file parts.
	MetaURL       = "url"
	MetaMediaType = "media_type"
	MetaTitle     = "title"
	MetaFilename  = "filename"
	MetaSourceID  = "source_id"
)

// Activity kinds the encoders understand.
const (
	// ActivityReplaceContent means "everything streamed so far in this run is
	// replaced by Value.content" (Nanite's direct-return and promissory-preamble
	// recovery). aisdk renders it as reset-step, nanitelegacy as replace_content,
	// agui as a custom event.
	ActivityReplaceContent = "chatstream.replace_content"
	// ActivityStateSnapshot and ActivityStateDelta carry AG-UI style state:
	// Value is the snapshot, Patch an RFC 6902 patch.
	ActivityStateSnapshot = "chatstream.state.snapshot"
	ActivityStateDelta    = "chatstream.state.delta"
)

// MetaString returns Event.Meta[key] as a string ("" when absent or not a JSON
// string).
func (e Event) MetaString(key string) string {
	raw, ok := e.Meta[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// MetaBool returns Event.Meta[key] as a bool (false when absent or not a JSON
// bool).
func (e Event) MetaBool(key string) bool {
	raw, ok := e.Meta[key]
	if !ok {
		return false
	}
	var b bool
	return json.Unmarshal(raw, &b) == nil && b
}
