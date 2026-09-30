// Package openaicompat encodes chatstream events as OpenAI-style
// chat.completion.chunk SSE, for clients written against the OpenAI (and
// OpenRouter, Ollama, vLLM) streaming API. The stream ends with "data: [DONE]".
//
// Mapping (chatstream event -> chunk):
//
//	run.start                    role chunk: delta {role: assistant, content: ""}
//	part text delta              delta {content}
//	part reasoning delta         delta {reasoning_content} (the de-facto extension of
//	                             DeepSeek/OpenRouter; standard clients ignore it)
//	part refusal delta           delta {refusal}
//	part tool_call               delta {tool_calls: [{index, id, type: function,
//	                             function: {name, arguments: ""}}]}, then argument
//	                             fragments {tool_calls: [{index, function: {arguments}}]}
//	                             (whole arguments in Final become one fragment)
//	run.finish                   finish_reason chunk (see FinishReason), then a usage
//	                             chunk (choices: []) when usage was reported
//	run.error                    {"error": {message, type: server_error, code}}
//	run.abort                    {"error": {message, type: aborted, code: run_aborted}}
//	gap                          an SSE comment line
//
// Not representable in an assistant chat stream and dropped: tool results,
// approvals, source/file/data parts, activity and raw events, steps and
// messages.
//
// Usage is converted back from disjoint components to OpenAI's inclusive
// counts: prompt_tokens is every prompt token (cache reads and writes included),
// completion_tokens every generated token (reasoning included), with
// prompt_tokens_details.cached_tokens the cache reads and
// completion_tokens_details.reasoning_tokens the reasoning tokens. Cache writes
// have no OpenAI field and are inside prompt_tokens only.
package openaicompat
