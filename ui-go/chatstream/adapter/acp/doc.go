// Package acp decodes the client side of an Agent Client Protocol (ACP v1)
// connection into chatstream events: newline-delimited JSON-RPC 2.0 messages
// from an ACP agent, for one session/prompt turn.
//
// # What one stream is
//
// ACP has no stream of its own. Streaming output is session/update
// notifications the agent sends while the client's session/prompt request is
// outstanding, and the turn ends with the JSON-RPC response to that request.
// The decoder is fed every line the agent writes and maps it as follows.
//
// session/update, by update.sessionUpdate:
//
//	agent_message_chunk   text part (see contiguity below)
//	agent_thought_chunk   reasoning part
//	user_message_chunk    raw (it is history the agent echoes, not this turn)
//	tool_call             tool_call part; rawInput, when present, is one
//	                      json_fragment delta and the part's Final
//	tool_call_update      completed/failed: a tool_result part; otherwise an
//	                      activity "acp.tool_call_update"
//	plan                  activity "acp.plan"
//	available_commands_update, current_mode_update, config_option_update,
//	session_info_update, usage_update
//	                      activity "acp.available_commands", "acp.current_mode",
//	                      "acp.config_options", "acp.session_info", "acp.usage"
//	anything else         raw
//
// A run of chunks of one kind (with the same messageId, or none) is ONE part:
// it opens on the first chunk and closes when a different session/update
// arrives, or the messageId changes (which also ends the message and starts a
// new one). Chunks whose content is not text (image, audio, resource) are kept
// as raw events and do not break the run. usage_update reports context
// occupancy and cost, not token usage of the turn, so it is an activity and
// never a usage event.
//
// Agent-to-client requests: session/request_permission becomes an
// approval.request (mode in-band, approval id the JSON-RPC id as text, call id
// the tool call's id, descriptor the whole params, so the offered options are
// there). Every other agent request (fs/*, terminal/*, elicitation/*) is raw.
//
// Responses end the turn: a result with a stopReason is the terminal event
// (end_turn stop, max_tokens length, max_turn_requests turn_limit, refusal
// refusal, cancelled a run.abort, anything else finish "other" with the agent's
// word in RawReason); a JSON-RPC error is a run.error with the error code as
// Code (-32800, request cancelled, is a run.abort). Any error response ends the
// turn, not only the prompt's: a failed initialize or session/new leaves nothing
// to decode. Other responses (initialize, session/new, session/load) are raw,
// and the sessionId in them or in an update goes in run.start's Ext under "acp".
// If the draft end-of-turn usage (result.usage) is present it is read as
// inclusive counts, the OpenAI convention, and rides on the run.finish.
//
// # Limits
//
//   - One decoder is one turn. Feed it the lines from the session/prompt
//     request on. session/load replays the whole history as session/update
//     notifications; the decoder cannot tell replayed history from the current
//     turn, so decode a load's replay with its own decoder and ignore the result.
//   - ACP v1 only. The v2 draft's state_update / idle terminal is not decoded.
//   - The decoder sees only what the agent writes, so it does not know which
//     request id is the prompt's: it takes a stopReason as the prompt's answer.
//
//nolint:misspell // "cancelled" is ACP's wire value for a stopReason
package acp
