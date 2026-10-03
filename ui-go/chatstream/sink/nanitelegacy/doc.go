// Package nanitelegacy encodes chatstream events as Nanite's chat.StreamEvent
// vocabulary, so existing Nanite clients (its GUI, `nanite chat`, the harness
// v1 consumers) can read a chatstream pipeline during a migration. It is a
// bridge, not a target to grow: new consumers should read the native stream.
//
// Framing is Nanite's own: `id: N` (omitted when the event has no Seq),
// `event: <type>`, `data: <StreamEvent JSON>` where the JSON carries `type` and,
// when the event has a Seq, `event_id`.
//
// Mapping (chatstream event -> Nanite event):
//
//	run.start                    stream_start {message_id = run id, agent_id from ext.nanite}
//	part text | refusal delta    delta {content, phase from part meta "phase"}
//	part reasoning delta         delta {content, phase: thinking}
//	part tool_call start         tool_call {tool, tool_id, detail}; arguments are not
//	                             streamed, Nanite has no argument stream
//	part tool_result end         tool_result {tool, tool_id, summary (500 B), is_error}
//	approval.request             approval_request {data: JSON string
//	                             {request_id, tool, input, reason}}
//	usage                        folded into stream_end
//	run.finish                   stream_end {message_id, usage}
//	run.error                    error {error, structured_error{code, message}}
//	run.abort                    status {content: "run aborted: ..."}, then stream_end
//	                             with the cancel stop_reason (Nanite has no abort)
//	raw with dialect "nanite"    the payload, unchanged, under its own event name
//	activity replace_content     replace_content {content}
//	activity, gap                status {content, detail}
//
// Dropped: raw events of other dialects, steps, messages, source/file/data parts,
// tool arguments. Nanite's usage counts input excluding cache (Anthropic-style),
// which is exactly UncachedInput: input_tokens = UncachedInput, cache_read_tokens
// and cache_creation_tokens are the cache components, output_tokens is
// Output+Reasoning.
package nanitelegacy
