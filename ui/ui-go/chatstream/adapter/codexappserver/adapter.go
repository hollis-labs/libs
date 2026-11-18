package codexappserver

import chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"

// DialectName is the adapter's name.
const DialectName = "codex.app-server"

// extKey is the Event.Ext key and the Raw.Dialect of codexappserver's events.
const extKey = "codex"

type adapter struct{}

// New returns the Codex app-server adapter.
func New() chatstream.Adapter { return adapter{} }

func (adapter) Name() string                { return DialectName }
func (adapter) Framing() chatstream.Framing { return chatstream.FramingJSONRPCLines }

// Capabilities: text and reasoning summaries stream as token deltas, tool
// arguments arrive whole, usage once per turn with cache and reasoning counts,
// and a turn can be interrupted (turn/interrupt), which ends it with run.abort.
// Approval requests are server requests the decoder only reports as activity,
// so no approval mode is claimed; there is no resume cursor.
func (adapter) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{
		Text:            chatstream.GranularityToken,
		ToolArgs:        chatstream.GranularityFinal,
		Tools:           true,
		Reasoning:       chatstream.ReasoningSummary,
		Usage:           chatstream.UsageAtEnd,
		CacheTokens:     true,
		ReasoningTokens: true,
		Cancel:          true,
		Framing:         chatstream.FramingJSONRPCLines,
	}
}

func (adapter) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder {
	return newDecoder(o)
}
