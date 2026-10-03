package openairesponses

import chatstream "github.com/hollis-labs/libs/ui-go/chatstream"

// Dialect is the adapter's name and the dialect on its raw events.
const Dialect = "openai.responses"

type adapter struct{}

// New returns the adapter for OpenAI Responses API streams.
func New() chatstream.Adapter { return adapter{} }

func (adapter) Name() string { return Dialect }

func (adapter) Framing() chatstream.Framing { return chatstream.FramingSSE }

// Capabilities are honest to the reference: text and function arguments stream
// token by token, reasoning is exposed as summaries and full text with an
// encrypted item to round-trip, usage arrives once at the end with cache and
// reasoning counts, annotations are citations, server-side tools exist, parts
// interleave, and background responses resume from a sequence number. There is
// no in-stream cancel, and approvals are in-band MCP approval requests.
func (adapter) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{
		Text:               chatstream.GranularityToken,
		Tools:              true,
		ToolArgs:           chatstream.GranularityToken,
		Reasoning:          chatstream.ReasoningFull,
		ReasoningRoundTrip: true,
		Usage:              chatstream.UsageAtEnd,
		CacheTokens:        true,
		ReasoningTokens:    true,
		Citations:          true,
		Refusal:            true,
		ServerTools:        true,
		InterleavedParts:   true,
		ResumeCursor:       true,
		Approval:           chatstream.ApprovalCapInBand,
		Framing:            chatstream.FramingSSE,
	}
}

func (adapter) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder { return newDecoder(o) }
