package claudejson

import chatstream "github.com/hollis-labs/libs/ui-go/chatstream"

// DialectName is the adapter's Name and the Raw.Dialect of its raw events.
const DialectName = "claude.stream-json"

type adapter struct{}

// New returns the Claude Code CLI stream-json adapter.
func New() chatstream.Adapter { return adapter{} }

func (adapter) Name() string                { return DialectName }
func (adapter) Framing() chatstream.Framing { return chatstream.FramingNDJSON }

// Capabilities are the guarantees that hold whatever flags the CLI was started
// with: text and tool arguments arrive at least once per content block (whole),
// though token by token with --include-partial-messages. Usage arrives once, at
// the end. Approvals are answered in band. Reasoning is a summary as the CLI
// presents it, and the CLI, not the caller, round-trips it.
func (adapter) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{
		Text: chatstream.GranularityFinal, ToolArgs: chatstream.GranularityFinal, Tools: true,
		Reasoning: chatstream.ReasoningSummary,
		Usage:     chatstream.UsageAtEnd, CacheTokens: true,
		Approval: chatstream.ApprovalCapInBand,
		Framing:  chatstream.FramingNDJSON,
	}
}

func (adapter) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder { return newDecoder(o) }
