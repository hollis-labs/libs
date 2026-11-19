// Package agy decodes Antigravity CLI print-mode stream-json output.
//
// This is a separate adapter, not a Claude JSON wrapper. The recorded frames
// use event rather than type, integer step_index identities rather than
// Anthropic blocks, and nested result.status rather than subtype/is_error.
// Reusing claudejson would preserve every line as raw and report truncation.
//
// Mapping:
//
//   - init: run.start (conversation_id) and agy.init activity.
//   - agent_response text_delta: part.delta, accumulated per step_index;
//     DONE closes with the complete JSON string in final.
//   - tool tool_info: whole tool_call parameters, followed by tool_result
//     when the upstream supplies completion information; identity is step_index.
//   - user_input and unknown steps: activity/raw, never assistant text.
//   - result.denied_actions: agy.permission_denied activity, no approval request.
//   - result SUCCESS: run.finish; ERROR/interrupted: run.abort;
//     other ERROR: run.error. A step failure alone is never terminal.
//   - missing result: run.error upstream_truncated, after closing accumulated
//     parts with their complete final content. Unknown/malformed lines: raw.
//
// See testdata/captures/PROVENANCE.md for executable identity, commands,
// redactions, observed protocol limitations and fixture derivations.
package agy
