package aisdk

import (
	"encoding/json"
	"net/http"
	"strings"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink"
)

// New returns an AI SDK UI message stream Encoder.
func New() sink.Encoder {
	return &encoder{
		parts: map[string]*partState{},
		tools: map[string]*toolState{},
		alias: map[string]string{},
	}
}

type partState struct {
	kind     chatstream.PartKind
	key      string // the part's id in the stream
	id       string // id used on the wire; a reopened text part gets a new one
	open     bool   // a text or reasoning block is open on the wire
	meta     chatstream.Event
	text     strings.Builder
	frags    strings.Builder
	streamed bool
	bound    bool // its tool-input was already sent by an approval
}

type toolState struct {
	name      string
	announced bool // opened by a part.start (not only by an approval)
	input     bool // tool-input-available sent
	done      bool
	approved  bool
}

type awaiting struct{ tool, callID string }

type encoder struct {
	sink.Bind
	sink.Lifecycle

	started  bool
	stepOpen bool
	stepGen  int

	parts    map[string]*partState
	order    []string // open part ids, oldest first
	tools    map[string]*toolState
	alias    map[string]string // part id -> tool call id it bound to
	awaiting []awaiting
	stepTool []string

	usage    *chatstream.Usage
	provider string
	model    string
	runID    string
	seq      int
}

func (e *encoder) Name() string        { return "aisdk" }
func (e *encoder) ContentType() string { return "text/event-stream" }
func (e *encoder) Headers() http.Header {
	h := sink.SSEHeaders()
	h.Set("x-vercel-ai-ui-message-stream", "v1")
	return h
}

type chunk map[string]any

func (e *encoder) send(w sink.Writer, c chunk) error {
	data, err := sink.Marshal(c)
	if err != nil {
		return err
	}
	return e.Send(w, "", "", data)
}

func (e *encoder) sendAll(w sink.Writer, cs []chunk) error {
	for _, c := range cs {
		if err := e.send(w, c); err != nil {
			return err
		}
	}
	return nil
}

func (e *encoder) begin(out *[]chunk) {
	if e.started {
		return
	}
	c := chunk{"type": "start"}
	md := chunk{}
	if e.runID != "" {
		md["runId"] = e.runID
	}
	if e.provider != "" {
		md["provider"] = e.provider
	}
	if e.model != "" {
		md["model"] = e.model
	}
	if len(md) > 0 {
		c["messageMetadata"] = md
	}
	*out = append(*out, c)
	e.started = true
}

func (e *encoder) openStep(out *[]chunk) {
	e.begin(out)
	if !e.stepOpen {
		*out = append(*out, chunk{"type": "start-step"})
		e.stepOpen = true
		e.stepGen++
	}
}

// closeParts ends every part still open on the wire, newest first, so a terminal
// chunk never leaves a text or reasoning block dangling.
func (e *encoder) closeParts(out *[]chunk) {
	for len(e.order) > 0 {
		id := e.order[len(e.order)-1]
		e.order = e.order[:len(e.order)-1]
		// The part stays known (closed on the wire): its later delta reopens it and
		// its part.end is not out of order.
		if p := e.parts[id]; p != nil {
			e.endBlock(out, p)
		}
	}
}

// endBlock ends p's open text or reasoning block on the wire.
func (e *encoder) endBlock(out *[]chunk, p *partState) {
	if !p.open {
		return
	}
	p.open = false
	switch p.kind {
	case chatstream.PartText, chatstream.PartRefusal:
		*out = append(*out, chunk{"type": "text-end", "id": p.id})
	case chatstream.PartReasoning:
		*out = append(*out, chunk{"type": "reasoning-end", "id": p.id})
	default: // only text-like parts have a block
	}
}

// reopenBlock starts a new block for p after a step or an approval closed the old
// one, under a new wire id so the client never sees an id restart.
func (e *encoder) reopenBlock(out *[]chunk, p *partState) {
	e.openStep(out)
	p.open = true
	e.order = append(e.order, p.key)
	if p.kind == chatstream.PartReasoning {
		p.id = e.nextID("reasoning")
		*out = append(*out, chunk{"type": "reasoning-start", "id": p.id})
		return
	}
	p.id = e.nextID("text")
	*out = append(*out, chunk{"type": "text-start", "id": p.id})
}

func (e *encoder) closeStep(out *[]chunk) {
	e.closeParts(out)
	if e.stepOpen {
		*out = append(*out, chunk{"type": "finish-step"})
	}
	e.stepOpen = false
	e.stepTool = nil
}

func (e *encoder) nextID(prefix string) string {
	e.seq++
	return prefix + "-" + itoa(e.seq)
}

func itoa(n int) string {
	b, _ := sink.Marshal(n)
	return string(b)
}

func (e *encoder) Encode(w sink.Writer, ev chatstream.Event) error {
	if err := e.Admit(); err != nil {
		return err
	}
	if ev.RunID != "" {
		e.runID = ev.RunID
	}
	var out []chunk
	if err := e.mapEvent(&out, ev); err != nil {
		return err
	}
	if err := e.sendAll(w, out); err != nil {
		return err
	}
	if ev.IsTerminal() {
		e.SetTerminal()
	}
	return nil
}

func (e *encoder) mapEvent(out *[]chunk, ev chatstream.Event) error {
	switch ev.Verb {
	case chatstream.VerbRunStart:
		e.provider, e.model = ev.Provider, ev.Model
		e.begin(out)
	case chatstream.VerbStepStart:
		if e.stepOpen {
			e.closeStep(out)
		}
		e.openStep(out)
	case chatstream.VerbStepFinish:
		if e.stepOpen {
			e.closeStep(out)
		}
	case chatstream.VerbMessageStart, chatstream.VerbMessageEnd:
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
	case chatstream.VerbActivity:
		e.activity(out, ev)
	case chatstream.VerbRaw:
		if ev.Raw != nil {
			e.openStep(out)
			*out = append(*out, chunk{"type": "data-raw", "transient": true,
				"data": chunk{"dialect": ev.Raw.Dialect, "type": ev.Raw.Type, "payload": ev.Raw.Payload}})
		}
	case chatstream.VerbGap:
		e.openStep(out)
		*out = append(*out, chunk{"type": "data-gap", "data": chunk{"from": ev.From, "to": ev.To, "reason": ev.Reason}})
	case chatstream.VerbRunFinish:
		if ev.Usage != nil {
			e.foldUsage(ev)
		}
		e.begin(out)
		e.closeStep(out)
		c := chunk{"type": "finish", "finishReason": FinishReason(ev.Finish())}
		md := chunk{}
		if e.usage != nil {
			md["usage"] = UsageJSON(*e.usage)
		}
		if ev.RawReason != "" {
			md["rawFinishReason"] = ev.RawReason
		}
		if len(md) > 0 {
			c["messageMetadata"] = md
		}
		*out = append(*out, c)
	case chatstream.VerbRunError:
		e.closeStep(out)
		text := ev.Message
		if text == "" {
			text = ev.Code
		}
		if text == "" {
			text = "the run failed"
		}
		*out = append(*out, chunk{"type": "error", "errorText": text})
	case chatstream.VerbRunAbort:
		e.begin(out)
		e.closeStep(out)
		c := chunk{"type": "abort"}
		if ev.Reason != "" {
			c["reason"] = ev.Reason
		}
		*out = append(*out, c)
	}
	return nil
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

func (e *encoder) partStart(out *[]chunk, ev chatstream.Event) error {
	if _, open := e.parts[ev.PartID]; open {
		return sink.OutOfOrder(ev, "part is already open")
	}
	e.openStep(out)
	kind := ev.PartKind()
	p := &partState{kind: kind, key: ev.PartID, id: ev.PartID, meta: ev}
	switch kind {
	case chatstream.PartText, chatstream.PartRefusal:
		e.trackPart(p)
		*out = append(*out, chunk{"type": "text-start", "id": p.id})
	case chatstream.PartReasoning:
		e.trackPart(p)
		*out = append(*out, chunk{"type": "reasoning-start", "id": p.id})
	case chatstream.PartToolCall:
		name := sink.MetaString(ev, sink.MetaName)
		if name == "" {
			name = "tool"
		}
		// A call an approval already created binds to that part instead of
		// opening a second one. A call id an earlier part.start already used is
		// a reuse after part.end (Validate allows it): a new call.
		if t, known := e.tools[ev.PartID]; known && !t.announced {
			t.announced = true
			for i, a := range e.awaiting {
				if a.callID == ev.PartID {
					e.awaiting = append(e.awaiting[:i], e.awaiting[i+1:]...)
					break
				}
			}
			p.bound = true
			e.parts[ev.PartID] = p
			return nil
		}
		for i, a := range e.awaiting {
			if a.tool == name {
				e.alias[ev.PartID] = a.callID
				e.awaiting = append(e.awaiting[:i], e.awaiting[i+1:]...)
				p.bound = true
				e.parts[ev.PartID] = p
				return nil
			}
		}
		delete(e.alias, ev.PartID)
		e.tools[ev.PartID] = &toolState{name: name, announced: true}
		e.stepTool = append(e.stepTool, ev.PartID)
		e.parts[ev.PartID] = p
		*out = append(*out, chunk{"type": "tool-input-start", "toolCallId": ev.PartID, "toolName": name, "dynamic": true})
	default:
		e.parts[ev.PartID] = p
	}
	return nil
}

func (e *encoder) trackPart(p *partState) {
	p.open = true
	e.parts[p.key] = p
	e.order = append(e.order, p.key)
}

func (e *encoder) callID(partID string) string {
	if id, ok := e.alias[partID]; ok {
		return id
	}
	return partID
}

func (e *encoder) partDelta(out *[]chunk, ev chatstream.Event) error {
	p, ok := e.parts[ev.PartID]
	if !ok {
		return sink.OutOfOrder(ev, "part is not open")
	}
	switch p.kind {
	case chatstream.PartText, chatstream.PartRefusal:
		if !p.open {
			e.reopenBlock(out, p)
		}
		*out = append(*out, chunk{"type": "text-delta", "id": p.id, "delta": ev.Text})
	case chatstream.PartReasoning:
		if !p.open {
			e.reopenBlock(out, p)
		}
		*out = append(*out, chunk{"type": "reasoning-delta", "id": p.id, "delta": ev.Text})
	case chatstream.PartToolCall:
		p.frags.WriteString(ev.JSONFragment)
		p.streamed = p.streamed || ev.JSONFragment != ""
		if !p.bound && ev.JSONFragment != "" {
			*out = append(*out, chunk{"type": "tool-input-delta", "toolCallId": p.id, "inputTextDelta": ev.JSONFragment})
		}
	default:
		p.text.WriteString(ev.Text)
		p.frags.WriteString(ev.JSONFragment)
	}
	return nil
}

func (e *encoder) partEnd(out *[]chunk, ev chatstream.Event) error {
	p, ok := e.parts[ev.PartID]
	if !ok {
		return sink.OutOfOrder(ev, "part is not open")
	}
	delete(e.parts, ev.PartID)
	e.removeOrder(ev.PartID)
	switch p.kind {
	case chatstream.PartText, chatstream.PartRefusal:
		e.endBlock(out, p)
	case chatstream.PartReasoning:
		if !p.open && len(ev.Final) > 0 {
			// The final value rides on reasoning-end: reopen so it is not lost.
			e.reopenBlock(out, p)
			e.removeOrder(ev.PartID)
		}
		p.open = false
		c := chunk{"type": "reasoning-end", "id": p.id}
		if len(ev.Final) > 0 {
			c["providerMetadata"] = chunk{"chatstream": chunk{"final": ev.Final}}
		}
		*out = append(*out, c)
	case chatstream.PartToolCall:
		e.toolInput(out, p, ev)
	case chatstream.PartToolResult:
		e.toolResult(out, p, ev)
	case chatstream.PartSource:
		if url := sink.MetaString(p.meta, sink.MetaURL); url != "" {
			c := chunk{"type": "source-url", "sourceId": firstNonEmpty(sink.MetaString(p.meta, sink.MetaSourceID), p.id), "url": url}
			if t := sink.MetaString(p.meta, sink.MetaTitle); t != "" {
				c["title"] = t
			}
			*out = append(*out, c)
		} else {
			c := chunk{"type": "source-document", "sourceId": firstNonEmpty(sink.MetaString(p.meta, sink.MetaSourceID), p.id),
				"mediaType": sink.MetaString(p.meta, sink.MetaMediaType), "title": sink.MetaString(p.meta, sink.MetaTitle)}
			if f := sink.MetaString(p.meta, sink.MetaFilename); f != "" {
				c["filename"] = f
			}
			*out = append(*out, c)
		}
	case chatstream.PartFile:
		*out = append(*out, chunk{"type": "file", "url": sink.MetaString(p.meta, sink.MetaURL), "mediaType": sink.MetaString(p.meta, sink.MetaMediaType)})
	case chatstream.PartData:
		name := firstNonEmpty(sink.MetaString(p.meta, sink.MetaDataName), "chatstream")
		var data any
		switch {
		case len(ev.Final) > 0:
			data = ev.Final
		case p.text.Len() > 0:
			data = p.text.String()
		case p.frags.Len() > 0 && json.Valid([]byte(p.frags.String())):
			data = json.RawMessage(p.frags.String())
		}
		*out = append(*out, chunk{"type": "data-" + name, "id": p.id, "data": data})
	}
	return nil
}

func (e *encoder) removeOrder(id string) {
	for i, v := range e.order {
		if v == id {
			e.order = append(e.order[:i], e.order[i+1:]...)
			return
		}
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (e *encoder) toolInput(out *[]chunk, p *partState, ev chatstream.Event) {
	id := e.callID(p.id)
	t := e.tools[id]
	if t == nil || t.input {
		return
	}
	t.input = true
	*out = append(*out, chunk{"type": "tool-input-available", "toolCallId": id, "toolName": t.name, "input": toolInputValue(p, ev), "dynamic": true})
}

func toolInputValue(p *partState, ev chatstream.Event) any {
	if len(ev.Final) > 0 {
		return ev.Final
	}
	if s := p.frags.String(); s != "" && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return map[string]any{}
}

func (e *encoder) toolResult(out *[]chunk, p *partState, ev chatstream.Event) {
	callID := sink.MetaString(p.meta, sink.MetaCallID)
	if callID == "" {
		callID = p.id
	}
	callID = e.callID(callID)
	// A result for a call the encoder no longer tracks would make the client
	// throw, so recreate the part first.
	t := e.tools[callID]
	if t == nil {
		name := firstNonEmpty(sink.MetaString(p.meta, sink.MetaName), "tool")
		t = &toolState{name: name, input: true}
		e.tools[callID] = t
		e.stepTool = append(e.stepTool, callID)
		*out = append(*out, chunk{"type": "tool-input-available", "toolCallId": callID, "toolName": name, "input": map[string]any{}, "dynamic": true})
	}
	if t.done {
		return
	}
	t.done = true
	var output any
	switch {
	case len(ev.Final) > 0:
		output = ev.Final
	default:
		output = p.text.String()
	}
	if sink.MetaBool(p.meta, sink.MetaIsError) {
		text := p.text.String()
		if text == "" && len(ev.Final) > 0 {
			text = string(ev.Final)
		}
		if text == "" {
			text = "Tool call failed"
		}
		*out = append(*out, chunk{"type": "tool-output-error", "toolCallId": callID, "errorText": text, "dynamic": true})
		return
	}
	*out = append(*out, chunk{"type": "tool-output-available", "toolCallId": callID, "output": output, "dynamic": true})
}

// approvalDescriptor is the conventional shape of approval.request's Descriptor:
// {"tool": name, "input": {...}}. Anything else is passed through opaque.
type approvalDescriptor struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

func (e *encoder) approval(out *[]chunk, ev chatstream.Event) {
	e.openStep(out)
	e.closeText(out)
	var d approvalDescriptor
	_ = json.Unmarshal(ev.Descriptor, &d)

	callID := ev.CallID
	if callID != "" {
		callID = e.callID(callID)
	}
	if callID == "" || e.tools[callID] == nil {
		if callID == "" {
			// Prefer the newest unfinished call of the same tool: CLI runtimes
			// announce the call first.
			for i := len(e.stepTool) - 1; i >= 0; i-- {
				id := e.stepTool[i]
				t := e.tools[id]
				if t != nil && !t.done && !t.approved && t.name == d.Tool {
					callID = id
					break
				}
			}
		}
		if callID == "" || e.tools[callID] == nil {
			if callID == "" {
				callID = "approval-" + ev.ApprovalID
			}
			name := firstNonEmpty(d.Tool, "tool")
			var input any = map[string]any{}
			if len(d.Input) > 0 {
				input = d.Input
			}
			e.tools[callID] = &toolState{name: name, input: true}
			e.stepTool = append(e.stepTool, callID)
			*out = append(*out, chunk{"type": "tool-input-available", "toolCallId": callID, "toolName": name, "input": input, "dynamic": true})
			// The call itself is announced later; let it bind here.
			e.awaiting = append(e.awaiting, awaiting{tool: name, callID: callID})
		}
	}
	e.tools[callID].approved = true
	c := chunk{"type": "tool-approval-request", "toolCallId": callID, "approvalId": ev.ApprovalID}
	if ev.Reason != "" {
		c["reason"] = ev.Reason
	}
	if len(ev.Descriptor) > 0 {
		c["approvalDescriptor"] = ev.Descriptor
	}
	*out = append(*out, c)
}

// closeText ends open text and reasoning parts before a tool card appears, the
// way the spike does, so a tool never renders inside a text block.
func (e *encoder) closeText(out *[]chunk) {
	// The parts stay known, closed on the wire: a later delta reopens a block
	// under a new id and the part's own end is still in order, so nothing is lost.
	for _, id := range e.order {
		if p := e.parts[id]; p != nil {
			e.endBlock(out, p)
		}
	}
	e.order = nil
}

func (e *encoder) activity(out *[]chunk, ev chatstream.Event) {
	e.openStep(out)
	if ev.Kind == sink.ActivityReplaceContent {
		e.closeText(out)
		*out = append(*out, chunk{"type": "reset-step"})
		for _, id := range e.stepTool {
			delete(e.tools, id)
		}
		for pid, cid := range e.alias {
			for _, id := range e.stepTool {
				if cid == id {
					delete(e.alias, pid)
				}
			}
		}
		keep := e.awaiting[:0:0]
		for _, a := range e.awaiting {
			dropped := false
			for _, id := range e.stepTool {
				dropped = dropped || a.callID == id
			}
			if !dropped {
				keep = append(keep, a)
			}
		}
		e.awaiting = keep
		e.stepTool = nil
		var v struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(ev.Value, &v)
		if v.Content != "" {
			id := e.nextID("text")
			*out = append(*out,
				chunk{"type": "text-start", "id": id},
				chunk{"type": "text-delta", "id": id, "delta": v.Content},
				chunk{"type": "text-end", "id": id})
		}
		return
	}
	d := chunk{"kind": ev.Kind}
	if len(ev.Value) > 0 {
		d["value"] = ev.Value
	}
	if len(ev.Patch) > 0 {
		d["patch"] = ev.Patch
	}
	*out = append(*out, chunk{"type": "data-activity", "transient": true, "data": d})
}

// Close writes "data: [DONE]". If the run had not ended it first closes the open
// step and writes an error chunk, so the client learns the run is over.
func (e *encoder) Close(w sink.Writer, cause error) error {
	already, ended := e.Closing()
	if already {
		return nil
	}
	if !ended {
		var out []chunk
		e.begin(&out)
		e.closeStep(&out)
		out = append(out, chunk{"type": "error", "errorText": sink.CauseText(cause)})
		if err := e.sendAll(w, out); err != nil {
			return err
		}
	}
	return e.Send(w, "", "", []byte("[DONE]"))
}

// FinishReason maps a chatstream FinishReason to the AI SDK's: stop, length,
// content-filter, tool-calls, error, other. Refusal is content-filter (the
// nearest); pause, context_exceeded, turn_limit and cancel are other.
func FinishReason(r chatstream.FinishReason) string {
	switch r {
	case chatstream.FinishStop:
		return "stop"
	case chatstream.FinishLength:
		return "length"
	case chatstream.FinishToolCalls:
		return "tool-calls"
	case chatstream.FinishContentFilter, chatstream.FinishRefusal:
		return "content-filter"
	case chatstream.FinishError:
		return "error"
	default:
		return "other"
	}
}

// UsageJSON is u in the AI SDK's LanguageModelUsage shape: inputTokens counts
// every prompt token, outputTokens every generated token, and the detail
// objects break them down.
func UsageJSON(u chatstream.Usage) map[string]any {
	return map[string]any{
		"inputTokens": u.Input(),
		"inputTokenDetails": map[string]any{
			"noCacheTokens":    u.UncachedInput,
			"cacheReadTokens":  u.CacheRead,
			"cacheWriteTokens": u.CacheWrite,
		},
		"outputTokens": u.Generated(),
		"outputTokenDetails": map[string]any{
			"textTokens":      u.Output,
			"reasoningTokens": u.Reasoning,
		},
		"totalTokens": u.Total(),
	}
}
