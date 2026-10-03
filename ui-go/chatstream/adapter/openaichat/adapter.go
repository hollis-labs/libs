package openaichat

import chatstream "github.com/hollis-labs/libs/ui-go/chatstream"

// Dialect is the adapter's name and the dialect on its raw events.
const Dialect = "openai.chat"

type adapter struct{}

// New returns the adapter for OpenAI Chat Completions streams.
func New() chatstream.Adapter { return adapter{} }

func (adapter) Name() string { return Dialect }

func (adapter) Framing() chatstream.Framing { return chatstream.FramingSSE }

// Capabilities are honest to the reference: text and tool arguments stream
// token by token, usage arrives once at the end (only when stream_options asks
// for it) with cached and reasoning counts, and there is no reasoning stream,
// no citations, no resume and no approval. Parts can be open together because
// tool calls stream by index.
func (adapter) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{
		Text:             chatstream.GranularityToken,
		Tools:            true,
		ToolArgs:         chatstream.GranularityToken,
		Usage:            chatstream.UsageAtEnd,
		CacheTokens:      true,
		ReasoningTokens:  true,
		Refusal:          true,
		InterleavedParts: true,
		Framing:          chatstream.FramingSSE,
	}
}

func (adapter) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder { return newDecoder(o) }
