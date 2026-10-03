// Package framing turns byte streams into chatstream.Frame sequences for a
// Decoder: SSE through go-ssekit's parser, and newline-delimited lines for the
// NDJSON and JSON-RPC-over-stdio dialects. It owns no wire format: SSE parsing
// is go-ssekit's (WHATWG line endings, comments, multi-line data, retry and
// id handling), and this package only adapts its events to Frames.
package framing
