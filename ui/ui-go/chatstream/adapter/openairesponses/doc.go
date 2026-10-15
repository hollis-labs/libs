// Package openairesponses decodes OpenAI Responses API streams (named SSE
// events whose JSON "type" matches the event name) into chatstream events.
//
// # Shape
//
// One run is one Response, and it is one chatstream message: message.start
// carries the response id and message.end closes it, and every part of every
// output item (text, reasoning, tool calls) lives inside it. Item ids, content
// and summary indexes appear in part ids and Meta so nothing is lost by
// flattening. sequence_number, the upstream's own resume cursor, is kept in
// Ext["openai"]["sequence_number"] of every event decoded from that frame; it is
// never the event's Seq, which the hub assigns.
//
// # Mapping
//
//	response.created                       run.start (+ message.start); response id, model in Ext
//	response.queued                        activity "openai.response.queued"
//	response.in_progress                   nothing (opens the run if created was missed)
//	response.output_item.added/done       see below, by item type
//	response.content_part.added/done      part start / end (output_text -> text, refusal, reasoning_text -> reasoning)
//	response.output_text.delta/done       part.delta / part.end (part opened on first delta when content_part.added was missed)
//	response.output_text.annotation.added source part (start+end) with the annotation in Meta
//	response.refusal.delta/done           refusal part
//	response.reasoning_summary_part.*     reasoning part, Meta kind "summary"
//	response.reasoning_summary_text.*     its deltas
//	response.reasoning_text.*             reasoning part, Meta kind "full"
//	response.function_call_arguments.*    json fragments of the tool_call part; Final = the arguments when valid JSON
//	response.custom_tool_call_input.*     same, Final = the input as a JSON string
//	response.mcp_call_arguments.*         same as function calls
//	response.*_call.in_progress / searching / interpreting / generating / completed / failed,
//	response.mcp_list_tools.*, response.compaction.compacting
//	                                       activity "openai.<event>" {item_id, output_index}
//	response.code_interpreter_call_code.*, response.image_generation_call.partial_image,
//	response.shell_call_*, response.audio.*
//	                                       raw (not modeled; capabilities say so)
//	response.completed                     run.finish (or run.error / run.abort, see below)
//	response.incomplete                    run.finish with the reason below
//	response.failed, error                 run.error, terminal
//	anything else                          raw, never terminal
//
// Output items: a function_call, custom_tool_call or mcp_call item is a tool_call
// part (Meta name, call_id, item_id, type), finalized by its arguments .done event
// or, failing that, by output_item.done; an mcp_call that carries output or an
// error also yields a tool_result part; an mcp_approval_request is an
// approval.request with mode inband; mcp_list_tools is an activity; a reasoning
// item with encrypted_content yields a reasoning part whose part.end Final is the
// whole item, the opaque value to send back with the next request; every other
// item type (web_search_call, file_search_call, code_interpreter_call,
// image_generation_call, computer_call, shell_call, compaction, ...) is a data
// part whose Final is the finished item.
//
// # Terminal events
//
// response.completed is run.finish with reason tool_calls when the response's
// output contains a function_call or custom_tool_call, else stop. Usage goes on
// the run.finish through chatstream.UsageFromInclusive, because input_tokens
// includes cached tokens and output_tokens includes reasoning tokens.
// response.incomplete is run.finish too (an incomplete response is a normal,
// reported end, not an error), with reason from incomplete_details.reason:
//
//	max_output_tokens -> length      (max_tokens, seen in the reference example, too)
//	max_messages      -> turn_limit
//	content_filter    -> content_filter
//	steered           -> other       (RawReason "steered"; a successor response follows)
//	anything else     -> other
//
// A response that carries an error, or has status failed, is run.error whichever
// event delivered it, with the error code (server_error and rate_limit_exceeded
// and vector_store_timeout are retryable; every other documented code is not),
// and one whose status is the cancel status is run.abort. That last case is not a
// stream event in the reference; it is handled in case a server sends it.
//
// # Termination and unknown frames
//
// The terminal events above are the dialect's only terminal signal. "data:
// [DONE]", which the reference does not document for this API, is ignored. A
// stream that ends before one of them is truncated: Close closes what is open
// and emits run.error with chatstream.CodeUpstreamTruncated, even after a
// usage-bearing event. A frame that is not valid JSON, or whose type is unknown,
// is preserved as a raw event and decoding continues; it is never terminal.
package openairesponses
