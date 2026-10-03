// Package claudejson decodes the Claude Code CLI's `--output-format stream-json`
// output (newline-delimited JSON) into chatstream events. The dialect name is
// "claude.stream-json".
//
// One line is one Frame. The decoder needs no flags beyond the CLI's own: with
// --include-partial-messages the text and tool arguments stream token by token
// (stream_event lines wrap the raw Anthropic Messages events, decoded by the
// same state machine as package anthropic, shared through
// internal/anthropicwire); without it every content block arrives whole in an
// assistant message.
//
// # Only result ends the run
//
// The CLI reports problems in many ways that are not the end of the run:
// system/api_retry before a retry, assistant.error, rate_limit_event, tool
// errors inside tool_result blocks. The Nanite CLI path and the spike's mapper
// each treated an error-looking line as terminal; this decoder does not. The
// terminal event is decided by the result line alone. If the process ends
// without one, Close emits run.error with code upstream_truncated.
//
// # Line mapping
//
//	system/init           run.start (the session id is the run id when
//	                      DecodeOptions.RunID is empty; the model; the whole
//	                      init object in Ext.claude)
//	system/<other>        activity, kind "claude.system.<subtype>", Value = the line
//	stream_event          the wrapped Anthropic event, through anthropic.Machine:
//	                      message_start = step.start + message.start,
//	                      message_stop = message.end + step.finish (the run does
//	                      not end), blocks as in package anthropic. Usage is not
//	                      taken from it: each model call restarts the cumulative
//	                      counters, and the result line carries the run's total.
//	assistant             whole content blocks as parts, unless the message was
//	                      already streamed (the CLI sends both). text, thinking,
//	                      redacted_thinking, tool_use, server_tool_use and
//	                      *_tool_result blocks map as in package anthropic; a
//	                      whole block is part.start, one part.delta, part.end.
//	                      Every tool_call part has Meta name and id.
//	                      aborted=true and error become activity events
//	                      ("claude.assistant_aborted", "claude.assistant_error").
//	user                  tool_result blocks become tool_result parts inside a
//	                      user message, Meta call_id = the part id of the
//	                      tool_call part with that tool_use_id, is_error when the
//	                      block says so; a result whose call was never seen
//	                      cannot name one and becomes a data part. A user message
//	                      with no tool_result is a raw event
//	control_request       can_use_tool (a tool_name, or subtype can_use_tool)
//	                      becomes approval.request, mode inband, ApprovalID the
//	                      request_id, CallID the part id of the tool_call it
//	                      gates (empty when that call was not seen; the raw
//	                      tool_use_id stays in the Descriptor), Descriptor the
//	                      request; other control requests are raw events
//	control_response      raw event
//	result                a final usage (in run.finish, or a usage event before
//	                      a run.error or run.abort) and the terminal event, below
//	anything else         raw event (dialect "claude.stream-json")
//
// A line that is not JSON is a raw event; decoding continues.
//
// # The result line
//
// terminal_reason (or, when absent, subtype error_max_turns as max_turns) picks
// the outcome, first match wins:
//
//	aborted_streaming, aborted_tools     run.abort, Reason = terminal_reason
//	max_turns                            run.finish, turn_limit
//	prompt_too_long                      run.finish, context_exceeded
//	is_error, or subtype other than      run.error, code = subtype (else
//	  success                              terminal_reason), message = errors[]
//	                                       or the result text; Retryable for
//	                                       api_error and model_error
//	otherwise                            run.finish: completed maps by the API
//	                                       stop_reason (end_turn/stop_sequence
//	                                       stop, max_tokens length, tool_use
//	                                       tool_calls, pause_turn pause, refusal
//	                                       refusal, model_context_window_exceeded
//	                                       context_exceeded, none stop); any other
//	                                       terminal_reason (tool_deferred,
//	                                       hook_stopped, background_requested, ...)
//	                                       is finish reason other
//
// RawReason is the word the reason was mapped from: terminal_reason, except that
// for a completed run (or one with none) it is the API stop_reason when there is
// one. Empty text blocks in assistant lines are skipped. Cost, turn
// count, duration, denials and errors are in Ext.claude of the terminal event.
//
// # Usage
//
// result.usage is the run's total, in Anthropic's exclusive-input shape
// (input_tokens excludes cache), so it maps to the disjoint components with
// chatstream.UsageFromExclusiveInput. It is the only usage this decoder reports,
// with scope final.
package claudejson
