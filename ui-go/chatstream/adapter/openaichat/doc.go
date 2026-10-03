// Package openaichat decodes OpenAI Chat Completions streams
// (chat.completion.chunk objects over SSE, ending in "data: [DONE]") into
// chatstream events. The same dialect is spoken by most OpenAI-compatible
// servers (OpenRouter, Ollama, vLLM, DeepSeek), which is why decoding is
// tolerant where the reference is silent.
//
// # Mapping
//
//   - The first chunk emits run.start (id, model, system_fingerprint and
//     service_tier in Ext["openai"]) and message.start (role assistant).
//   - delta.content is one text part, opened on the first non-empty fragment;
//     delta.refusal is a refusal part; delta.reasoning_content and delta.reasoning
//     (sent by some compatible servers, never by OpenAI) are a reasoning part. The
//     dialect's declared Capabilities say it streams no reasoning, because the
//     reference has no such field; the decoder decodes it when a server sends it.
//     At most one of these content parts is open at a time.
//   - delta.tool_calls[] is one tool_call part per index, keyed to the index by a
//     map. The part id is the tool call id (call-<index> when a server sends
//     none), Meta carries name and index, and arguments stream as JSON fragments.
//     part.end's Final is the arguments when they are valid JSON; otherwise it is
//     absent and Ext["openai"]["arguments_valid"] is false, the accumulated
//     fragments remain the record, and nothing is repaired. The deprecated
//     delta.function_call is decoded as a tool call with part id "function_call".
//   - A non-null finish_reason closes every open part and the message. The run's
//     terminal event waits for [DONE].
//   - The usage chunk (choices empty, usage set; sent after finish_reason when
//     stream_options.include_usage is on) becomes the final usage on run.finish.
//     prompt_tokens includes cached tokens and completion_tokens includes
//     reasoning tokens, so it goes through chatstream.UsageFromInclusive.
//   - An {"error":{...}} frame is run.error, terminal.
//   - Choices with index > 0 (request n > 1) are preserved as raw events and not
//     decoded; the model of this module is one assistant message per run.
//
// # Finish reasons
//
//	stop            -> stop
//	length          -> length
//	content_filter  -> content_filter
//	tool_calls      -> tool_calls
//	function_call   -> tool_calls (deprecated)
//	anything else   -> other
//
// The upstream word is always Event.RawReason.
//
// # Termination
//
// The terminal signal of this dialect is [DONE], not finish_reason. A stream
// that ends without it is truncated: Close closes what is open and emits
// run.error with chatstream.CodeUpstreamTruncated, even when a finish_reason
// (and even a usage chunk) had already arrived. That is the case the audit found
// swallowed silently. A consumer that knows its server omits [DONE] can read the
// error's Ext["openai"]["finish_reason_seen"], which names the finish reason the
// stream had reached, and decide for itself. A usage report received before the
// truncation is emitted as a final usage event ahead of the error, so it is not
// lost. [DONE] with no finish_reason before it is run.finish with reason other.
//
// # Frames the decoder does not understand
//
// A frame that is not a JSON object is preserved as a raw event (type
// "malformed", payload the frame as a JSON string) and decoding continues; it is
// never terminal. Chunk, choice and delta keys it does not know are preserved in
// one raw event of type "unknown_fields", and a moderation chunk in one of type
// "moderation". Padding (obfuscation) and logprobs are dropped.
package openaichat
