package agy

import (
	"bytes"
	"encoding/json"
	"fmt"

	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/internal/decodekit"
)

type decoder struct {
	*decodekit.Base
	parts    map[string]*part
	steps    map[int]*stepState
	textSeen bool
}
type part struct {
	text  string
	final json.RawMessage
}
type stepState struct {
	kind, id string
	done     bool
	called   bool
}
type envelope struct {
	Event          string  `json:"event"`
	ConversationID string  `json:"conversation_id"`
	Step           *step   `json:"step_update"`
	Result         *result `json:"result"`
}
type step struct {
	ConversationID string          `json:"conversation_id"`
	Index          *int            `json:"step_index"`
	State          string          `json:"state"`
	Type           string          `json:"step_type"`
	Text           string          `json:"text_delta"`
	ToolName       string          `json:"tool_name"`
	ToolInfo       json.RawMessage `json:"tool_info"`
}
type result struct {
	ConversationID string          `json:"conversation_id"`
	Status         string          `json:"status"`
	Response       string          `json:"response"`
	Error          string          `json:"error"`
	FinishedReason string          `json:"finished_reason"`
	Usage          json.RawMessage `json:"usage"`
	Denied         json.RawMessage `json:"denied_actions"`
}

func newDecoder(o chatstream.DecodeOptions) *decoder {
	return &decoder{Base: decodekit.New(o), parts: map[string]*part{}, steps: map[int]*stepState{}}
}
func encode(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (d *decoder) raw(out []chatstream.Event, typ string, b []byte) []chatstream.Event {
	return d.Emit(out, d.RawEvent(DialectName, typ, b))
}
func (d *decoder) activity(out []chatstream.Event, kind string, b json.RawMessage) []chatstream.Event {
	e := d.Event(chatstream.VerbActivity)
	e.Kind = "agy." + kind
	e.Value = b
	return d.Emit(out, e)
}
func (d *decoder) message(out []chatstream.Event) []chatstream.Event {
	if !d.MessageOpen() {
		e := d.Event(chatstream.VerbMessageStart)
		e.MessageID = "assistant"
		e.Role = "assistant"
		out = d.Emit(out, e)
	}
	return out
}
func (d *decoder) start(out []chatstream.Event, id string, kind chatstream.PartKind, meta map[string]json.RawMessage, final json.RawMessage) []chatstream.Event {
	out = d.message(out)
	d.parts[id] = &part{final: final}
	e := d.Event(chatstream.VerbPartStart)
	e.PartID = id
	e.Kind = string(kind)
	e.Meta = meta
	return d.Emit(out, e)
}
func (d *decoder) end(out []chatstream.Event, id string) []chatstream.Event {
	if !d.PartOpen(id) {
		return out
	}
	p := d.parts[id]
	e := d.Event(chatstream.VerbPartEnd)
	e.PartID = id
	if p.final != nil {
		e.Final = p.final
	} else {
		e.Final = encode(p.text)
	}
	out = d.Emit(out, e)
	delete(d.parts, id)
	return out
}
func (d *decoder) unwind(out []chatstream.Event) []chatstream.Event {
	ids := d.OpenParts()
	for i := len(ids) - 1; i >= 0; i-- {
		out = d.end(out, ids[i])
	}
	return d.Unwind(out)
}
func (d *decoder) Close(cause error) []chatstream.Event {
	if d.Closed() || d.Terminated() {
		return d.Base.Close(cause)
	}
	out := d.EnsureStarted(nil)
	out = d.unwind(out)
	return append(out, d.Base.Close(cause)...)
}
func (d *decoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	if d.Terminated() || len(bytes.TrimSpace(f.Data)) == 0 {
		return nil, nil
	}
	var h envelope
	if err := json.Unmarshal(f.Data, &h); err != nil {
		return d.raw(d.EnsureStarted(nil), "malformed", f.Data), nil
	}
	if !d.Started() {
		id := h.ConversationID
		if h.Step != nil {
			id = h.Step.ConversationID
		}
		if h.Result != nil {
			id = h.Result.ConversationID
		}
		d.SetRunID(id)
	}
	out := d.EnsureStarted(nil)
	switch h.Event {
	case "init":
		return d.activity(out, "init", append(json.RawMessage(nil), f.Data...)), nil
	case "step_update":
		if h.Step == nil || h.Step.Index == nil || *h.Step.Index < 0 {
			return d.raw(out, "step_update", f.Data), nil
		}
		return d.update(out, *h.Step, f.Data), nil
	case "result":
		if h.Result == nil {
			return d.raw(out, "result", f.Data), nil
		}
		return d.finish(out, *h.Result, f.Data), nil
	default:
		return d.raw(out, h.Event, f.Data), nil
	}
}
func (d *decoder) update(out []chatstream.Event, s step, raw []byte) []chatstream.Event {
	if s.Type != "agent_response" && s.Type != "tool" {
		return d.activity(out, "step_update", append(json.RawMessage(nil), raw...))
	}
	st := d.steps[*s.Index]
	if st == nil {
		// Bound identity bookkeeping even when upstream never sends a result.
		if len(d.steps) >= 4096 {
			out = d.unwind(out)
			return d.LimitExceeded(out, "Agy steps", 4096)
		}
		st = &stepState{kind: s.Type, id: fmt.Sprintf("step-%d", *s.Index)}
		d.steps[*s.Index] = st
	}
	if st.kind != s.Type {
		return d.raw(out, "step_update", raw)
	}
	if st.done {
		return d.raw(out, "step_update.repeated", raw)
	}
	if s.Type == "tool" {
		return d.tool(out, st, s, raw)
	}
	id := st.id + ":text"
	if s.Text != "" {
		if !d.PartOpen(id) {
			out = d.start(out, id, chatstream.PartText, nil, nil)
		}
		d.parts[id].text += s.Text
		d.textSeen = true
		e := d.Event(chatstream.VerbPartDelta)
		e.PartID = id
		e.Text = s.Text
		out = d.Emit(out, e)
	}
	if s.State == "DONE" || s.State == "ERROR" {
		out = d.end(out, id)
		st.done = true
	}
	return out
}
func (d *decoder) tool(out []chatstream.Event, st *stepState, s step, raw []byte) []chatstream.Event {
	var info struct {
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
		Output     json.RawMessage `json:"output"`
		Error      json.RawMessage `json:"error"`
	}
	if json.Unmarshal(s.ToolInfo, &info) != nil {
		return d.raw(out, "step_update.tool", raw)
	}
	name := info.Name
	if name == "" {
		name = s.ToolName
	}
	if name == "" || len(info.Parameters) == 0 || string(info.Parameters) == "null" {
		return d.raw(out, "step_update.tool", raw)
	}
	call := st.id + ":call"
	if !st.called {
		// Parameters arrive whole, not as streamed JSON fragments.
		var compact bytes.Buffer
		if json.Compact(&compact, info.Parameters) != nil {
			return d.raw(out, "step_update.tool", raw)
		}
		params := append(json.RawMessage(nil), compact.Bytes()...)
		out = d.start(out, call, chatstream.PartToolCall, map[string]json.RawMessage{chatstream.MetaName: encode(name)}, params)
		e := d.Event(chatstream.VerbPartDelta)
		e.PartID = call
		e.JSONFragment = string(params)
		out = d.Emit(out, e)
		out = d.end(out, call)
		st.called = true
	}
	if s.State != "DONE" && s.State != "ERROR" {
		return out
	}
	st.done = true
	final := info.Output
	isError := s.State == "ERROR" || (len(info.Error) > 0 && string(info.Error) != "null")
	if len(info.Error) > 0 && string(info.Error) != "null" {
		final = info.Error
	}
	// Denied commands can be marked DONE with no output or error. Preserve
	// that completion as activity; do not fabricate a successful tool result.
	if len(final) == 0 || string(final) == "null" {
		return d.activity(out, "tool_completion", append(json.RawMessage(nil), raw...))
	}
	id := st.id + ":result"
	out = d.start(out, id, chatstream.PartToolResult, map[string]json.RawMessage{chatstream.MetaCallID: encode(call), chatstream.MetaIsError: encode(isError)}, append(json.RawMessage(nil), final...))
	return d.end(out, id)
}
func (d *decoder) finish(out []chatstream.Event, r result, raw []byte) []chatstream.Event {
	if r.Status != "SUCCESS" && r.Status != "ERROR" {
		return d.raw(out, "result", raw)
	}
	if len(r.Denied) > 0 && string(r.Denied) != "null" && string(r.Denied) != "[]" {
		out = d.activity(out, "permission_denied", r.Denied)
	}
	// A result response is a whole-turn summary of already streamed text. Only
	// use it when no text delta arrived; replaying it would duplicate content.
	if !d.textSeen && r.Response != "" {
		out = d.start(out, "result:text", chatstream.PartText, nil, nil)
		d.parts["result:text"].text = r.Response
		e := d.Event(chatstream.VerbPartDelta)
		e.PartID = "result:text"
		e.Text = r.Response
		out = d.Emit(out, e)
	}
	out = d.unwind(out)
	u := d.usage(r.Usage)
	if u == nil && len(r.Usage) > 0 && string(r.Usage) != "null" {
		out = d.raw(out, "result.usage", r.Usage)
	}
	e := d.Event(chatstream.VerbRunFinish)
	if r.Status == "SUCCESS" {
		e.Reason = string(chatstream.FinishStop)
		e.RawReason = r.Status
		e.Usage = u
	} else {
		if u != nil {
			ue := d.Event(chatstream.VerbUsage)
			ue.Usage = u
			out = d.Emit(out, ue)
		}
		if r.FinishedReason == "interrupted" {
			e.Verb = chatstream.VerbRunAbort
			e.Reason = "interrupted"
		} else {
			e.Verb = chatstream.VerbRunError
			e.Code = r.FinishedReason
			if e.Code == "" {
				e.Code = "agy_error"
			}
			e.Message = r.Error
		}
	}
	return d.Emit(out, e)
}
func (d *decoder) usage(raw json.RawMessage) *chatstream.Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var u struct {
		Input    int `json:"input_tokens"`
		Output   int `json:"output_tokens"`
		Thinking int `json:"thinking_tokens"`
		Cache    int `json:"cache_read_tokens"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return nil
	}
	// Agy input excludes cache reads, while output includes thinking. See
	// the recorded tool_success step where cache reads exceed input.
	usage := chatstream.Usage{Scope: chatstream.UsageFinal, UncachedInput: u.Input, CacheRead: u.Cache, Output: u.Output - u.Thinking, Reasoning: u.Thinking}
	if usage.Valid() != nil {
		return nil
	}
	return &usage
}
