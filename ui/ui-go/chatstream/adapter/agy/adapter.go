package agy

import chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"

// DialectName identifies Agy's newline-delimited stream-json protocol.
const DialectName = "agy.stream-json"

type adapter struct{}

// New returns an adapter for one Agy print-mode turn.
func New() chatstream.Adapter               { return adapter{} }
func (adapter) Name() string                { return DialectName }
func (adapter) Framing() chatstream.Framing { return chatstream.FramingNDJSON }

// Capabilities reflect the recorded protocol: small text deltas, whole tool
// parameters, final usage and process cancellation, with no approval bridge.
func (adapter) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{
		Text: chatstream.GranularityToken, ToolArgs: chatstream.GranularityFinal,
		Tools: true, Usage: chatstream.UsageAtEnd, CacheTokens: true,
		ReasoningTokens: true, Cancel: true, Framing: chatstream.FramingNDJSON,
	}
}
func (adapter) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder { return newDecoder(o) }
