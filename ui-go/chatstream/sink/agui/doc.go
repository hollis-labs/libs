// Package agui encodes chatstream events as AG-UI (1.0) events: SSE frames whose
// data is one event JSON object. AG-UI consumers ignore SSE fields other than
// data, so the frame id also carries the event's Seq, when assigned, for
// clients that resume by Last-Event-ID; AG-UI itself has no resumption.
//
// Mapping (chatstream event -> AG-UI events):
//
//	run.start                    RUN_STARTED {threadId, runId, parentRunId}
//	step.start / step.finish     STEP_STARTED / STEP_FINISHED {stepName}
//	message.start / message.end  (none: AG-UI messages are the text parts)
//	part text | refusal          TEXT_MESSAGE_START / _CONTENT / _END {messageId = part id}
//	part reasoning               REASONING_START, REASONING_MESSAGE_START/_CONTENT/_END,
//	                             REASONING_END; a Final becomes REASONING_ENCRYPTED_VALUE
//	part tool_call               TOOL_CALL_START / _ARGS / _END (whole arguments in Final
//	                             become one _ARGS)
//	part tool_result             TOOL_CALL_RESULT {messageId, toolCallId, content, role: tool}
//	approval.request inband      CUSTOM chatstream.approval_request
//	approval.request suspend     recorded; the run finishes with an interrupt outcome
//	usage                        recorded; RUN_FINISHED / RUN_ERROR carry usage
//	activity                     ACTIVITY_SNAPSHOT / ACTIVITY_DELTA (stable id per kind);
//	                             state kinds become STATE_SNAPSHOT / STATE_DELTA
//	raw                          RAW {event, source}
//	gap                          CUSTOM chatstream.gap
//	run.finish                   RUN_FINISHED (outcome: cancel, interrupt, or absent)
//	run.error                    RUN_ERROR {message, code, usage}
//	run.abort                    RUN_FINISHED {outcome: {type: "cancel" outcome}}
//
// AG-UI requires everything a run opened to be closed before RUN_FINISHED and a
// terminal signal distinguishable from truncation: the encoder closes what is
// still open first, and Close on a run that never ended writes RUN_ERROR.
package agui
