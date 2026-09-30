// Package anthropic decodes the Anthropic Messages API streaming format (SSE)
// into chatstream events. The dialect name is "anthropic.messages".
//
// The block-by-block state machine is shared with adapter/claudejson through
// internal/anthropicwire. The decoder consumes chatstream.Frames whose Data is one event's JSON (the
// JSON "type" field, or failing that the SSE event name, selects the event). It
// makes no HTTP calls and has no SDK dependency.
//
// # Event mapping
//
//	message_start        run.start (model; the message id is the run id when
//	                     DecodeOptions.RunID is empty), message.start, and a
//	                     cumulative usage event when the message carries usage
//	content_block_start  part.start; the part id is "<message id>:<index>"
//	content_block_delta  part.delta (see below)
//	content_block_stop   part.end (Final carries what the block accumulated)
//	message_delta        cumulative usage event; the stop reason is remembered
//	message_stop         open parts closed, message.end, run.finish with the
//	                     mapped FinishReason, RawReason and final usage
//	error                open parts closed, run.error (terminal)
//	ping                 ignored
//	anything else        raw event (dialect "anthropic")
//
// Blocks and deltas:
//
//	text                    text part;         text_delta -> Text
//	thinking                reasoning part;    thinking_delta -> Text;
//	                                           signature_delta -> Final {"signature":...}
//	redacted_thinking       reasoning part, Meta redacted=true, Final {"data":...}
//	                        (the audit found Nanite drops these; they must be
//	                        round-tripped, so they are captured)
//	tool_use                tool_call part, Meta {name, id};
//	                        input_json_delta -> JSONFragment; Final is the
//	                        parsed arguments when they are valid JSON, and
//	                        Ext.anthropic.args_invalid=true when they are not
//	                        (fine-grained tool streaming can end mid-value)
//	server_tool_use         tool_call part, Meta {id, name, server: true}
//	*_tool_result           tool_result part, Meta {call_id, block_type,
//	                        tool_use_id, server: true}; call_id is the part id of
//	                        the tool_call part it answers, and is_error is true
//	                        when the block says is_error or its content is a
//	                        *_error object. The whole block is Final. A result
//	                        for a tool use this stream never showed cannot name a
//	                        call, so it is a data part instead (Meta block_type,
//	                        tool_use_id), never a tool_result with a dangling
//	                        call_id
//	citations_delta         a source part opened and closed inside the text part,
//	                        Final is the citation
//	compaction, fallback,   data part, Meta {block_type}; the whole block is
//	container_upload, ...   Final; compaction_delta text is appended
//	a delta of another type raw event
//
// # Stop reasons
//
//	end_turn -> stop              max_tokens -> length
//	stop_sequence -> stop         tool_use -> tool_calls
//	pause_turn -> pause           refusal -> refusal
//	model_context_window_exceeded -> context_exceeded
//	anything else (including compaction) -> other
//
// RawReason always carries Anthropic's own word; stop_details and stop_sequence
// travel in Ext.anthropic of the run.finish event.
//
// # Usage
//
// Anthropic's input_tokens EXCLUDES cache reads and writes, so the components
// map one to one (chatstream.UsageFromExclusiveInput): UncachedInput,
// CacheRead, CacheWrite, Output. When output_tokens_details.thinking_tokens is
// reported, it is split out of Output into Reasoning (Total is unchanged).
// server_tool_use request counts go to Usage.Extra. message_start and
// message_delta usage are cumulative and merged field by field, because
// message_delta may omit the input counts; run.finish carries the final usage.
//
// # Failure policy
//
// A frame that is not JSON becomes a raw event and decoding continues; it is
// not terminal. An unknown event type is a raw event. A delta for a block that
// is not open is a raw event. Blank data is ignored. An error event is
// terminal, with code = the error type and Retryable for overloaded_error,
// api_error, timeout_error and rate_limit_error. If the upstream ends before
// message_stop, Close closes what is open and emits run.error with code
// upstream_truncated; it never emits run.finish. Nothing is emitted after the
// terminal event.
package anthropic
