package openaicompat

import (
	"net/http"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink"
)

// New returns an OpenAI-compatible Encoder.
func New() sink.Encoder {
	return &encoder{parts: map[string]*partState{}}
}

type partState struct {
	kind     chatstream.PartKind
	index    int
	streamed bool
}

type encoder struct {
	sink.Bind
	sink.Lifecycle

	id, model string
	created   int64
	started   bool
	parts     map[string]*partState
	toolCount int
	usage     *chatstream.Usage
}

func (e *encoder) Name() string         { return "openaicompat" }
func (e *encoder) ContentType() string  { return "text/event-stream" }
func (e *encoder) Headers() http.Header { return sink.SSEHeaders() }

type obj map[string]any

func (e *encoder) chunk(delta obj, finish any) obj {
	return obj{
		"id": e.id, "object": "chat.completion.chunk", "created": e.created, "model": e.model,
		"choices": []obj{{"index": 0, "delta": delta, "finish_reason": finish}},
	}
}

func (e *encoder) begin(ev chatstream.Event, out *[]obj) {
	if e.started {
		return
	}
	e.started = true
	e.id = "chatcmpl-" + ev.RunID
	if ev.Time.Unix() > 0 {
		e.created = ev.Time.Unix()
	}
	if ev.Model != "" {
		e.model = ev.Model
	}
	*out = append(*out, e.chunk(obj{"role": "assistant", "content": ""}, nil))
}

func (e *encoder) Encode(w sink.Writer, ev chatstream.Event) error {
	if err := e.Admit(); err != nil {
		return err
	}
	var out []obj
	var comment string
	if err := e.mapEvent(&out, &comment, ev); err != nil {
		return err
	}
	if comment != "" {
		if err := e.Comment(w, comment); err != nil {
			return err
		}
	}
	for _, o := range out {
		data, err := sink.Marshal(o)
		if err != nil {
			return err
		}
		if err := e.Send(w, "", "", data); err != nil {
			return err
		}
	}
	if ev.IsTerminal() {
		e.SetTerminal()
	}
	return nil
}

func (e *encoder) mapEvent(out *[]obj, comment *string, ev chatstream.Event) error {
	switch ev.Verb {
	case chatstream.VerbRunStart:
		e.begin(ev, out)
	case chatstream.VerbPartStart:
		e.begin(ev, out)
		return e.partStart(out, ev)
	case chatstream.VerbPartDelta:
		return e.partDelta(out, ev)
	case chatstream.VerbPartEnd:
		return e.partEnd(out, ev)
	case chatstream.VerbUsage:
		e.foldUsage(ev)
	case chatstream.VerbGap:
		*comment = "gap " + itoa(ev.From) + ".." + itoa(ev.To) + " (" + ev.Reason + ")"
	case chatstream.VerbRunFinish:
		e.begin(ev, out)
		if ev.Usage != nil {
			e.foldUsage(ev)
		}
		reason := FinishReason(ev.Finish())
		if reason == "" {
			*out = append(*out, obj{"error": obj{
				"message": "the run finished with reason error", "type": "server_error", "code": "error"}})
			return nil
		}
		*out = append(*out, e.chunk(obj{}, reason))
		if e.usage != nil {
			*out = append(*out, obj{
				"id": e.id, "object": "chat.completion.chunk", "created": e.created, "model": e.model,
				"choices": []obj{}, "usage": UsageJSON(*e.usage),
			})
		}
	case chatstream.VerbRunError:
		code := ev.Code
		if code == "" {
			code = "error"
		}
		msg := ev.Message
		if msg == "" {
			msg = "the run failed"
		}
		*out = append(*out, obj{"error": obj{"message": msg, "type": "server_error", "code": code}})
	case chatstream.VerbRunAbort:
		msg := "run aborted"
		if ev.Reason != "" {
			msg += ": " + ev.Reason
		}
		*out = append(*out, obj{"error": obj{"message": msg, "type": "aborted", "code": "run_aborted"}})
	default: // steps, messages, approvals, activity and raw events have no chunk
	}
	return nil
}

func itoa(n uint64) string {
	b, _ := sink.Marshal(n)
	return string(b)
}

func (e *encoder) foldUsage(ev chatstream.Event) {
	if ev.Usage == nil {
		return
	}
	u := *ev.Usage
	if ev.Verb == chatstream.VerbUsage && u.Scope == chatstream.UsageDelta && e.usage != nil {
		sum := e.usage.Add(u)
		e.usage = &sum
		return
	}
	e.usage = &u
}

func (e *encoder) partStart(out *[]obj, ev chatstream.Event) error {
	if _, open := e.parts[ev.PartID]; open {
		return sink.OutOfOrder(ev, "part is already open")
	}
	p := &partState{kind: ev.PartKind()}
	e.parts[ev.PartID] = p
	if p.kind != chatstream.PartToolCall {
		return nil
	}
	p.index = e.toolCount
	e.toolCount++
	name := sink.MetaString(ev, sink.MetaName)
	*out = append(*out, e.chunk(obj{"tool_calls": []obj{{
		"index": p.index, "id": ev.PartID, "type": "function",
		"function": obj{"name": name, "arguments": ""},
	}}}, nil))
	return nil
}

func (e *encoder) partDelta(out *[]obj, ev chatstream.Event) error {
	p, ok := e.parts[ev.PartID]
	if !ok {
		return sink.OutOfOrder(ev, "part is not open")
	}
	switch p.kind {
	case chatstream.PartText:
		*out = append(*out, e.chunk(obj{"content": ev.Text}, nil))
	case chatstream.PartReasoning:
		*out = append(*out, e.chunk(obj{"reasoning_content": ev.Text}, nil))
	case chatstream.PartRefusal:
		*out = append(*out, e.chunk(obj{"refusal": ev.Text}, nil))
	case chatstream.PartToolCall:
		p.streamed = p.streamed || ev.JSONFragment != ""
		*out = append(*out, e.chunk(obj{"tool_calls": []obj{{"index": p.index, "function": obj{"arguments": ev.JSONFragment}}}}, nil))
	default: // tool results, sources, files and data are not part of an assistant chunk stream
	}
	return nil
}

func (e *encoder) partEnd(out *[]obj, ev chatstream.Event) error {
	p, ok := e.parts[ev.PartID]
	if !ok {
		return sink.OutOfOrder(ev, "part is not open")
	}
	delete(e.parts, ev.PartID)
	if p.kind == chatstream.PartToolCall && len(ev.Final) > 0 && !p.streamed {
		*out = append(*out, e.chunk(obj{"tool_calls": []obj{{"index": p.index, "function": obj{"arguments": string(ev.Final)}}}}, nil))
	}
	return nil
}

// Close writes "data: [DONE]". If the run had not ended it first writes an error
// frame carrying the cause.
func (e *encoder) Close(w sink.Writer, cause error) error {
	already, ended := e.Closing()
	if already {
		return nil
	}
	if !ended {
		data, err := sink.Marshal(obj{"error": obj{"message": sink.CauseText(cause), "type": "server_error", "code": chatstream.CodeStreamLost}})
		if err != nil {
			return err
		}
		if err := e.Send(w, "", "", data); err != nil {
			return err
		}
	}
	return e.Send(w, "", "", []byte("[DONE]"))
}

// FinishReason maps a chatstream FinishReason to OpenAI's finish_reason:
// stop, length, tool_calls or content_filter. Refusal, pause, cancel and
// other are stop; context_exceeded and turn_limit are length. FinishError has no
// finish_reason: it returns "" and the encoder writes an error frame instead.
func FinishReason(r chatstream.FinishReason) string {
	switch r {
	case chatstream.FinishLength, chatstream.FinishContextExceeded, chatstream.FinishTurnLimit:
		return "length"
	case chatstream.FinishToolCalls:
		return "tool_calls"
	case chatstream.FinishContentFilter:
		return "content_filter"
	case chatstream.FinishError:
		return ""
	default:
		return "stop"
	}
}

// UsageJSON is u in OpenAI's inclusive shape.
func UsageJSON(u chatstream.Usage) map[string]any {
	return map[string]any{
		"prompt_tokens":             u.Input(),
		"completion_tokens":         u.Generated(),
		"total_tokens":              u.Total(),
		"prompt_tokens_details":     map[string]any{"cached_tokens": u.CacheRead},
		"completion_tokens_details": map[string]any{"reasoning_tokens": u.Reasoning},
	}
}
