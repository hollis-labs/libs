package fakeserver_test

import (
	"encoding/json"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/internal/decodekit"
)

// echo is a tiny dialect for these tests: {"t":"text","v":...},
// {"t":"tool","id":...,"args":...}, {"t":"done"}. (A copy of the idea in the
// conformance kit's own tests; test code cannot be imported.)
type echo struct{}

func (echo) Name() string                { return "echo" }
func (echo) Framing() chatstream.Framing { return chatstream.FramingSSE }
func (echo) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{Framing: chatstream.FramingSSE, Text: chatstream.GranularityChunk, Tools: true, ToolArgs: chatstream.GranularityFinal}
}
func (echo) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder {
	return &echoDecoder{Base: decodekit.New(o)}
}

type echoDecoder struct {
	*decodekit.Base
	textOpen bool
}

func (d *echoDecoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	var m struct{ T, V, ID, Args string }
	if err := json.Unmarshal(f.Data, &m); err != nil {
		// a frame that cannot be decoded is preserved as a raw event, not an error
		return d.Emit(nil, d.RawEvent("echo", "malformed", f.Data)), nil //nolint:nilerr
	}
	var out []chatstream.Event
	out = d.EnsureStarted(out)
	switch m.T {
	case "text":
		if !d.textOpen {
			s := d.Event(chatstream.VerbPartStart)
			s.PartID, s.Kind = "t", string(chatstream.PartText)
			out = d.Emit(out, s)
			d.textOpen = true
		}
		dl := d.Event(chatstream.VerbPartDelta)
		dl.PartID, dl.Text = "t", m.V
		out = d.Emit(out, dl)
	case "tool":
		s := d.Event(chatstream.VerbPartStart)
		s.PartID, s.Kind = m.ID, string(chatstream.PartToolCall)
		s.Meta = map[string]json.RawMessage{chatstream.MetaName: json.RawMessage(`"echo_tool"`)}
		out = d.Emit(out, s)
		en := d.Event(chatstream.VerbPartEnd)
		en.PartID, en.Final = m.ID, json.RawMessage(m.Args)
		out = d.Emit(out, en)
	case "done":
		out = d.Unwind(out)
		fin := d.Event(chatstream.VerbRunFinish)
		fin.Reason = string(chatstream.FinishStop)
		out = d.Emit(out, fin)
	}
	return out, nil
}

func (d *echoDecoder) Close(cause error) []chatstream.Event { return d.Base.Close(cause) }

// echoFrames is a complete stream: two text frames, a tool call, done.
func echoFrames() []chatstream.Frame {
	return []chatstream.Frame{
		{Event: "m", Data: []byte(`{"t":"text","v":"Hello"}`)},
		{Event: "m", Data: []byte(`{"t":"text","v":", world"}`)},
		{Event: "m", Data: []byte(`{"t":"tool","id":"c1","args":"{\"a\":1}"}`)},
		{Event: "m", Data: []byte(`{"t":"done"}`)},
	}
}
