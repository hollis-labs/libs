// Package anthropicwire is the block state machine of the Anthropic Messages
// stream (message_start through message_stop, block by block), shared by the
// two adapters that decode it: adapter/anthropic reads the API's SSE stream, and
// adapter/claudejson reads the same events wrapped in the Claude CLI's
// stream_event lines. It also holds the usage merge (Anthropic's input_tokens
// excludes cache, so the counts map one to one to chatstream.Usage) and the
// stop-reason table. It is internal: adapters, not applications, use it.
package anthropicwire
