package acp

import chatstream "github.com/hollis-labs/go-chatstream"

// DialectName is the adapter's name and the Raw.Dialect / Ext key of its events.
const DialectName = "acp"

type adapter struct{}

// New returns the ACP adapter.
func New() chatstream.Adapter { return adapter{} }

func (adapter) Name() string                { return DialectName }
func (adapter) Framing() chatstream.Framing { return chatstream.FramingJSONRPCLines }

// Capabilities are ACP v1's: message and thought text arrive in chunks, tool
// arguments whole (rawInput), no token usage in the stable schema, permission
// requests are in-band JSON-RPC requests, session/cancel exists, and
// session/load replays history rather than resuming from a cursor.
func (adapter) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{
		Text:      chatstream.GranularityChunk,
		ToolArgs:  chatstream.GranularityFinal,
		Tools:     true,
		Reasoning: chatstream.ReasoningFull,
		Refusal:   true,
		Cancel:    true,
		Approval:  chatstream.ApprovalCapInBand,
		Framing:   chatstream.FramingJSONRPCLines,
	}
}

func (adapter) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder { return newDecoder(o) }
