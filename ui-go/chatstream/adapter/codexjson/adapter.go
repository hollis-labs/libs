package codexjson

import chatstream "github.com/hollis-labs/go-chatstream"

// DialectName is the adapter's name.
const DialectName = "codex.exec-json"

// extKey is the Event.Ext key and the Raw.Dialect of codexjson's events.
const extKey = "codex"

type config struct {
	baseline *chatstream.Usage
}

// Option configures the adapter.
type Option func(*config)

// WithCumulativeUsage says the stream's turn.completed counters are cumulative
// across the thread, and baseline is what the thread had used before this turn
// (the zero Usage for a new thread). The run.finish then reports the change.
func WithCumulativeUsage(baseline chatstream.Usage) Option {
	return func(c *config) { c.baseline = &baseline }
}

type adapter struct{ cfg config }

// New returns the Codex `exec --json` adapter.
func New(opts ...Option) chatstream.Adapter {
	var a adapter
	for _, o := range opts {
		o(&a.cfg)
	}
	return a
}

func (adapter) Name() string                { return DialectName }
func (adapter) Framing() chatstream.Framing { return chatstream.FramingNDJSON }

// Capabilities: whole messages and whole tool arguments, a reasoning summary
// without a signature, usage once per turn with cache and reasoning counts, no
// approval events and no documented cancel or resume cursor.
func (adapter) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{
		Text:            chatstream.GranularityFinal,
		ToolArgs:        chatstream.GranularityFinal,
		Tools:           true,
		Reasoning:       chatstream.ReasoningSummary,
		Usage:           chatstream.UsageAtEnd,
		CacheTokens:     true,
		ReasoningTokens: true,
		Framing:         chatstream.FramingNDJSON,
	}
}

func (a adapter) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder {
	return newDecoder(o, a.cfg)
}
