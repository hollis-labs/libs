// Package aisdk encodes chatstream events as the Vercel AI SDK UI message
// stream (protocol v1): SSE data frames, one JSON chunk each, ending with
// "data: [DONE]". Header x-vercel-ai-ui-message-stream: v1.
//
// Mapping (chatstream event -> chunk):
//
//	run.start                      start {messageMetadata: {runId, provider, model}}
//	step.start / step.finish       start-step / finish-step (a step is opened lazily
//	                               before content; at most one is open at a time)
//	message.start / message.end    (no chunk: a UI stream is one message)
//	part.start text | refusal      text-start {id}
//	part.start reasoning           reasoning-start {id}
//	part.start tool_call           tool-input-start {toolCallId, toolName, dynamic}
//	part.delta                     text-delta / reasoning-delta {id, delta};
//	                               tool-input-delta {toolCallId, inputTextDelta}
//	part.end text | refusal        text-end
//	part.end reasoning             reasoning-end (+ providerMetadata.chatstream.final)
//	part.end tool_call             tool-input-available {toolCallId, toolName, input, dynamic}
//	part.end tool_result           tool-output-available | tool-output-error
//	part source / file / data      source-url|source-document / file / data-<name>
//	approval.request               tool-approval-request (converges on the tool part)
//	usage                          nothing; folded into finish's messageMetadata
//	run.finish                     finish-step, finish {finishReason, messageMetadata}
//	run.error                      finish-step, error {errorText}
//	run.abort                      finish-step, abort {reason}
//	activity ActivityReplaceContent reset-step (then the replacement text)
//	activity, raw, gap             data-activity, data-raw (transient), data-gap
//
// The AI SDK has no refusal part, so a refusal is streamed as text. It has no
// cancel finish reason, so run.abort is the abort chunk.
//
// Approval and tool parts converge on one tool part in either order: Nanite's own
// loop asks before the call is announced, CLI runtimes announce the call first.
// An approval whose CallID names a known call attaches to it; without a CallID it
// attaches to the newest unfinished, unapproved call of the same tool; otherwise
// the part is created from the approval and the later part.start of that
// call binds to it instead of opening a second part.
package aisdk
