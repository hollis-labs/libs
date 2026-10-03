package anthropic

import (
	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/internal/anthropicwire"
)

// DialectName is the adapter's Name and the Raw.Dialect of its raw events.
const DialectName = anthropicwire.DialectName

// FinishReason maps Anthropic's stop_reason to chatstream's closed vocabulary
// (see the package documentation for the table). Callers that receive a raw
// stop_reason some other way, from a non-streaming response for instance, use
// the same mapping the decoder does.
func FinishReason(stopReason string) chatstream.FinishReason {
	return anthropicwire.FinishReason(stopReason)
}

type adapter struct{}

// New returns the Anthropic Messages streaming adapter.
func New() chatstream.Adapter { return adapter{} }

func (adapter) Name() string                { return DialectName }
func (adapter) Framing() chatstream.Framing { return chatstream.FramingSSE }

// Capabilities are what the dialect really provides: token-level text and tool
// arguments, full reasoning with a signature that must be returned, usage that
// arrives at the start and again cumulatively, cache and reasoning token counts,
// citations, refusals and server-side tools. Blocks arrive one at a time, but a
// citation is a source part nested inside its text part, hence InterleavedParts.
func (adapter) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{
		Text: chatstream.GranularityToken, ToolArgs: chatstream.GranularityToken, Tools: true,
		Reasoning: chatstream.ReasoningFull, ReasoningRoundTrip: true,
		Usage: chatstream.UsageIncremental, CacheTokens: true, ReasoningTokens: true,
		Citations: true, Refusal: true, ServerTools: true, InterleavedParts: true,
		Framing: chatstream.FramingSSE,
	}
}

func (adapter) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder { return newDecoder(o) }
