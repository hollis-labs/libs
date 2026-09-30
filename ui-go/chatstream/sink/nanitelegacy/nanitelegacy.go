package nanitelegacy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink"
)

// SummaryLimit is Nanite's cap on a tool_result summary, in bytes.
const SummaryLimit = 500

const truncated = "... (truncated)"

// stopCancelled is the stop_reason of a run that was aborted.
const stopCancelled = "cancelled" //nolint:misspell // the value Nanite clients see

// New returns a Nanite-legacy Encoder.
func New() sink.Encoder { return &encoder{parts: map[string]*partState{}} }

type partState struct {
	kind  chatstream.PartKind
	meta  chatstream.Event
	phase string
	text  strings.Builder
}

type encoder struct {
	sink.Bind
	sink.Lifecycle

	runID string
	parts map[string]*partState
	usage *chatstream.Usage
}

func (e *encoder) Name() string        { return "nanitelegacy" }
func (e *encoder) ContentType() string { return "text/event-stream" }
func (e *encoder) Headers() http.Header {
	h := sink.SSEHeaders()
	h.Set("Connection", "keep-alive")
	return h
}

type obj map[string]any

// frame is one legacy event: its type and JSON body (or verbatim payload).
type frame struct {
	typ  string
	body obj
	raw  json.RawMessage
}

func (e *encoder) Encode(w sink.Writer, ev chatstream.Event) error {
	if err := e.Admit(); err != nil {
		return err
	}
	if ev.RunID != "" {
		e.runID = ev.RunID
	}
	var out []frame
	if err := e.mapEvent(&out, ev); err != nil {
		return err
	}
	id := sink.FrameID(ev)
	for _, f := range out {
		if err := e.write(w, id, ev, f); err != nil {
			return err
		}
		id = ""
	}
	if ev.IsTerminal() {
		e.SetTerminal()
	}
	return nil
}

func (e *encoder) write(w sink.Writer, id string, ev chatstream.Event, f frame) error {
	var data []byte
	if f.raw != nil {
		var err error
		if data, err = compact(f.raw); err != nil {
			return err
		}
	} else {
		f.body["type"] = f.typ
		if ev.Seq != 0 && id != "" {
			f.body["event_id"] = ev.Seq
		}
		var err error
		if data, err = sink.Marshal(f.body); err != nil {
			return err
		}
	}
	return e.Send(w, id, f.typ, data)
}

// ErrInvalidPayload is returned by Encode for a nanite raw event whose Payload is
// not valid JSON. Such a payload is written as a data line, so a newline in it
// could smuggle in SSE fields; it is refused instead.
var ErrInvalidPayload = errors.New("nanitelegacy: raw payload is not valid JSON")

// compact is raw on one line; it fails when raw is not valid JSON.
func compact(raw json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	return b.Bytes(), nil
}

func (e *encoder) mapEvent(out *[]frame, ev chatstream.Event) error {
	switch ev.Verb {
	case chatstream.VerbRunStart:
		body := obj{"message_id": ev.RunID}
		if agent := extString(ev, "nanite", "agent_id"); agent != "" {
			body["agent_id"] = agent
		}
		*out = append(*out, frame{typ: "stream_start", body: body})
	case chatstream.VerbPartStart:
		return e.partStart(out, ev)
	case chatstream.VerbPartDelta:
		return e.partDelta(out, ev)
	case chatstream.VerbPartEnd:
		return e.partEnd(out, ev)
	case chatstream.VerbApprovalRequest:
		e.approval(out, ev)
	case chatstream.VerbUsage:
		e.foldUsage(ev)
	case chatstream.VerbRaw:
		if ev.Raw != nil && ev.Raw.Dialect == "nanite" && ev.Raw.Type != "" && len(ev.Raw.Payload) > 0 {
			*out = append(*out, frame{typ: ev.Raw.Type, raw: ev.Raw.Payload})
		}
	case chatstream.VerbActivity:
		if ev.Kind == sink.ActivityReplaceContent {
			var v struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal(ev.Value, &v)
			*out = append(*out, frame{typ: "replace_content", body: obj{"content": v.Content}})
			return nil
		}
		body := obj{"content": ev.Kind}
		// detail is a JSON string, so text that is not JSON is safe as it is.
		d := firstRaw(ev.Value, ev.Patch)
		if c, err := compact(d); err == nil {
			d = c
		}
		if len(d) > 0 {
			body["detail"] = string(d)
		}
		*out = append(*out, frame{typ: "status", body: body})
	case chatstream.VerbGap:
		*out = append(*out, frame{typ: "status", body: obj{
			"content": "events " + u(ev.From) + ".." + u(ev.To) + " are missing (" + ev.Reason + ")"}})
	case chatstream.VerbRunFinish:
		if ev.Usage != nil {
			e.foldUsage(ev)
		}
		*out = append(*out, frame{typ: "stream_end", body: e.streamEnd(StopReason(ev.Finish(), ev.RawReason))})
	case chatstream.VerbRunError:
		msg := ev.Message
		if msg == "" {
			msg = ev.Code
		}
		if msg == "" {
			msg = "the run failed"
		}
		*out = append(*out, frame{typ: "error", body: obj{
			"error": msg, "structured_error": obj{"code": ev.Code, "message": msg}}})
	case chatstream.VerbRunAbort:
		content := "run aborted"
		if ev.Reason != "" {
			content += ": " + ev.Reason
		}
		*out = append(*out,
			frame{typ: "status", body: obj{"content": content}},
			frame{typ: "stream_end", body: e.streamEnd(stopCancelled)})
	default: // steps and messages have no Nanite event
	}
	return nil
}

func u(n uint64) string {
	b, _ := sink.Marshal(n)
	return string(b)
}

func firstRaw(rs ...json.RawMessage) json.RawMessage {
	for _, r := range rs {
		if len(r) > 0 {
			return r
		}
	}
	return nil
}

func extString(ev chatstream.Event, ns, key string) string {
	raw, ok := ev.Ext[ns]
	if !ok {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(m[key], &s)
	return s
}

func (e *encoder) foldUsage(ev chatstream.Event) {
	if ev.Usage == nil {
		return
	}
	us := *ev.Usage
	if ev.Verb == chatstream.VerbUsage && us.Scope == chatstream.UsageDelta && e.usage != nil {
		sum := e.usage.Add(us)
		e.usage = &sum
		return
	}
	e.usage = &us
}

func (e *encoder) streamEnd(stop string) obj {
	body := obj{"message_id": e.runID}
	usage := obj{"input_tokens": 0, "output_tokens": 0}
	if e.usage != nil {
		usage["input_tokens"] = e.usage.UncachedInput
		usage["output_tokens"] = e.usage.Generated()
		if e.usage.CacheWrite > 0 {
			usage["cache_creation_tokens"] = e.usage.CacheWrite
		}
		if e.usage.CacheRead > 0 {
			usage["cache_read_tokens"] = e.usage.CacheRead
		}
	}
	if stop != "" {
		usage["stop_reason"] = stop
	}
	body["usage"] = usage
	return body
}

// StopReason is Nanite's stop_reason for a finish: the dialect's own word when
// it was kept (RawReason), else the Anthropic-style name Nanite's clients know:
// end_turn, max_tokens, tool_use; any other reason is passed through by name.
func StopReason(r chatstream.FinishReason, raw string) string {
	if raw != "" {
		return raw
	}
	switch r {
	case chatstream.FinishStop:
		return "end_turn"
	case chatstream.FinishLength:
		return "max_tokens"
	case chatstream.FinishToolCalls:
		return "tool_use"
	default:
		return string(r)
	}
}

func (e *encoder) partStart(out *[]frame, ev chatstream.Event) error {
	if _, open := e.parts[ev.PartID]; open {
		return sink.OutOfOrder(ev, "part is already open")
	}
	p := &partState{kind: ev.PartKind(), meta: ev, phase: sink.MetaString(ev, sink.MetaPhase)}
	e.parts[ev.PartID] = p
	if p.kind != chatstream.PartToolCall {
		return nil
	}
	body := obj{"tool": firstNonEmpty(sink.MetaString(ev, sink.MetaName), "tool"), "tool_id": ev.PartID}
	if d := sink.MetaString(ev, sink.MetaDetail); d != "" {
		body["detail"] = d
	}
	*out = append(*out, frame{typ: "tool_call", body: body})
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (e *encoder) partDelta(out *[]frame, ev chatstream.Event) error {
	p, ok := e.parts[ev.PartID]
	if !ok {
		return sink.OutOfOrder(ev, "part is not open")
	}
	switch p.kind {
	case chatstream.PartText, chatstream.PartRefusal:
		if ev.Text == "" {
			return nil
		}
		body := obj{"content": ev.Text}
		if p.phase != "" {
			body["phase"] = p.phase
		}
		*out = append(*out, frame{typ: "delta", body: body})
	case chatstream.PartReasoning:
		if ev.Text == "" {
			return nil
		}
		*out = append(*out, frame{typ: "delta", body: obj{"content": ev.Text, "phase": "thinking"}})
	case chatstream.PartToolResult:
		p.text.WriteString(ev.Text)
	default: // tool arguments are not streamed to Nanite clients
	}
	return nil
}

func (e *encoder) partEnd(out *[]frame, ev chatstream.Event) error {
	p, ok := e.parts[ev.PartID]
	if !ok {
		return sink.OutOfOrder(ev, "part is not open")
	}
	delete(e.parts, ev.PartID)
	if p.kind != chatstream.PartToolResult {
		return nil
	}
	summary := p.text.String()
	if summary == "" && len(ev.Final) > 0 {
		var s string
		if json.Unmarshal(ev.Final, &s) == nil {
			summary = s
		} else {
			summary = string(ev.Final)
		}
	}
	body := obj{
		"tool":    firstNonEmpty(sink.MetaString(p.meta, sink.MetaName), "tool"),
		"tool_id": firstNonEmpty(sink.MetaString(p.meta, sink.MetaCallID), ev.PartID),
		"summary": Truncate(summary),
	}
	if sink.MetaBool(p.meta, sink.MetaIsError) {
		body["is_error"] = true
	}
	*out = append(*out, frame{typ: "tool_result", body: body})
	return nil
}

// Truncate cuts s to Nanite's 500-byte summary limit, on a rune boundary, and
// appends "... (truncated)" when it cut.
func Truncate(s string) string {
	if len(s) <= SummaryLimit {
		return s
	}
	cut := SummaryLimit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + truncated
}

func (e *encoder) approval(out *[]frame, ev chatstream.Event) {
	var d struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(ev.Descriptor, &d)
	payload := obj{"request_id": ev.ApprovalID, "tool": d.Tool}
	if len(d.Input) > 0 {
		payload["input"] = d.Input
	}
	if ev.Reason != "" {
		payload["reason"] = ev.Reason
	}
	data, _ := sink.Marshal(payload)
	*out = append(*out, frame{typ: "approval_request", body: obj{"data": string(data)}})
}

// Close writes nothing when the run ended. Otherwise it writes an error event
// (structured_error code chatstream.CodeStreamLost) with the cause: Nanite's
// streams end in stream_end or error, and a client must get one of them.
func (e *encoder) Close(w sink.Writer, cause error) error {
	already, ended := e.Closing()
	if already || ended {
		return nil
	}
	msg := sink.CauseText(cause)
	body := obj{"type": "error", "error": msg, "structured_error": obj{"code": chatstream.CodeStreamLost, "message": msg}}
	data, err := sink.Marshal(body)
	if err != nil {
		return err
	}
	return e.Send(w, "", "error", data)
}
