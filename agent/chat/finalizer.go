package chat

import (
	"encoding/json"
	"strings"

	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
)

// Finalizer guarantees that a part.end carries the part's complete content in
// final. Pass one run's events through [Finalizer.Apply] in order: it
// accumulates each part's deltas and fills final on the part.end when the
// producer left it empty. The Service passes every runner's events through
// one, so a committed-only client can build a message from part.start and
// part.end alone.
//
// The shape of final per part kind:
//
//   - text, reasoning, refusal: an object with the complete text under
//     "text". A final the producer set as an object without "text" (a
//     reasoning signature, for example) keeps its keys and gains "text".
//   - tool_call: the call's full input, the JSON value its fragments spell.
//   - tool_result: an object with "text" and "is_error".
//
// A part that arrives whole, with no deltas, must carry its own final: the
// Finalizer has nothing to build it from and leaves the event as it is.
//
// The zero value is ready to use. A Finalizer is for one run and is not safe
// for concurrent use.
type Finalizer struct {
	parts map[string]*finalPart
}

type finalPart struct {
	kind    chatstream.PartKind
	isError bool
	deltas  bool
	text    strings.Builder
	args    strings.Builder
}

// Apply returns ev, with final filled when ev is the part.end of a part whose
// deltas passed through f.
func (f *Finalizer) Apply(ev chatstream.Event) chatstream.Event {
	switch ev.Verb { //nolint:exhaustive // only part events and terminals matter here
	case chatstream.VerbPartStart:
		if f.parts == nil {
			f.parts = map[string]*finalPart{}
		}
		f.parts[ev.PartID] = &finalPart{kind: ev.PartKind(), isError: ev.MetaBool(chatstream.MetaIsError)}
	case chatstream.VerbPartDelta:
		if p := f.parts[ev.PartID]; p != nil {
			p.deltas = p.deltas || ev.Text != "" || ev.JSONFragment != ""
			p.text.WriteString(ev.Text)
			p.args.WriteString(ev.JSONFragment)
		}
	case chatstream.VerbPartEnd:
		if p := f.parts[ev.PartID]; p != nil {
			delete(f.parts, ev.PartID)
			if p.deltas {
				ev.Final = p.final(ev.Final)
			}
		}
	case chatstream.VerbRunFinish, chatstream.VerbRunError, chatstream.VerbRunAbort:
		f.parts = nil
	}
	return ev
}

func (p *finalPart) final(given json.RawMessage) json.RawMessage {
	switch p.kind { //nolint:exhaustive // other kinds do not stream
	case chatstream.PartToolCall:
		if len(given) == 0 && json.Valid([]byte(p.args.String())) {
			return json.RawMessage(p.args.String())
		}
		return given
	case chatstream.PartText, chatstream.PartReasoning, chatstream.PartRefusal, chatstream.PartToolResult:
		obj := map[string]json.RawMessage{}
		if len(given) > 0 {
			if json.Unmarshal(given, &obj) != nil || obj == nil {
				return given // not an object: the producer's value stands
			}
			if _, ok := obj["text"]; ok {
				return given
			}
		}
		obj["text"] = mustJSON(p.text.String())
		if _, ok := obj["is_error"]; !ok && p.kind == chatstream.PartToolResult {
			obj["is_error"] = mustJSON(p.isError)
		}
		return mustJSON(obj)
	}
	return given
}

// HasFinal reports whether a part.end's final has the content the wire
// promises for a part of kind k: an object with a string "text" for text,
// reasoning, refusal and tool_result parts, any JSON value for a tool_call.
// Other kinds have no rule and always pass.
func HasFinal(k chatstream.PartKind, final json.RawMessage) bool {
	switch k { //nolint:exhaustive // other kinds have no rule
	case chatstream.PartToolCall:
		return len(final) > 0 && json.Valid(final)
	case chatstream.PartText, chatstream.PartReasoning, chatstream.PartRefusal, chatstream.PartToolResult:
		var obj struct {
			Text *string `json:"text"`
		}
		return len(final) > 0 && json.Unmarshal(final, &obj) == nil && obj.Text != nil
	}
	return true
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("chat: marshal " + err.Error()) // only strings, bools and maps of raw JSON reach here
	}
	return b
}
