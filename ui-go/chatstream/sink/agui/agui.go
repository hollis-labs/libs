package agui

import (
	"encoding/json"
	"net/http"
	"strings"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink"
)

// outcomeCancel is AG-UI's spelling of the cancel outcome type.
const outcomeCancel = "cancelled" //nolint:misspell // the wire value

// Option configures New.
type Option func(*encoder)

// WithThreadID sets the threadId of RUN_STARTED and RUN_FINISHED. AG-UI requires
// one and chatstream has no thread; the default is the run id.
func WithThreadID(id string) Option { return func(e *encoder) { e.threadID = id } }

// New returns an AG-UI Encoder.
func New(opts ...Option) sink.Encoder {
	e := &encoder{parts: map[string]*partState{}, steps: map[string]string{}}
	for _, o := range opts {
		o(e)
	}
	return e
}

type partState struct {
	kind     chatstream.PartKind
	meta     chatstream.Event
	text     strings.Builder
	streamed bool
}

type interrupt struct {
	id, reason, callID string
	expires            *string
}

type encoder struct {
	sink.Bind
	sink.Lifecycle

	threadID   string
	runID      string
	provider   string
	model      string
	parts      map[string]*partState
	order      []string
	steps      map[string]string // step id -> name
	stepOrder  []string
	interrupts []interrupt
	usage      *chatstream.Usage
	activities map[string]bool
}

func (e *encoder) Name() string         { return "agui" }
func (e *encoder) ContentType() string  { return "text/event-stream" }
func (e *encoder) Headers() http.Header { return sink.SSEHeaders() }

type obj map[string]any

func (e *encoder) thread() string {
	if e.threadID != "" {
		return e.threadID
	}
	return e.runID
}

func (e *encoder) Encode(w sink.Writer, ev chatstream.Event) error {
	if err := e.Admit(); err != nil {
		return err
	}
	if ev.RunID != "" {
		e.runID = ev.RunID
	}
	var out []obj
	if err := e.mapEvent(&out, ev); err != nil {
		return err
	}
	id := sink.FrameID(ev)
	for i := range out {
		if ms := sink.Milliseconds(ev.Time); ms != 0 {
			out[i]["timestamp"] = ms
		}
		if ev.Raw != nil && ev.Verb != chatstream.VerbRaw && i == 0 {
			out[i]["rawEvent"] = ev.Raw.Payload
		}
		if err := e.emit(w, id, out[i]); err != nil {
			return err
		}
		id = "" // one id per source event: the cursor is the event's, not each frame's
	}
	if ev.IsTerminal() {
		e.SetTerminal()
	}
	return nil
}

func (e *encoder) emit(w sink.Writer, id string, o obj) error {
	data, err := sink.Marshal(o)
	if err != nil {
		return err
	}
	return e.Send(w, id, "", data)
}

func (e *encoder) mapEvent(out *[]obj, ev chatstream.Event) error {
	switch ev.Verb {
	case chatstream.VerbRunStart:
		e.provider, e.model = ev.Provider, ev.Model
		o := obj{"type": "RUN_STARTED", "threadId": e.thread(), "runId": e.runID}
		if ev.ParentRunID != "" {
			o["parentRunId"] = ev.ParentRunID
		}
		*out = append(*out, o)
	case chatstream.VerbStepStart:
		name := ev.Name
		if name == "" {
			name = ev.StepID
		}
		if _, open := e.steps[ev.StepID]; open {
			return sink.OutOfOrder(ev, "step is already open")
		}
		e.steps[ev.StepID] = name
		e.stepOrder = append(e.stepOrder, ev.StepID)
		*out = append(*out, obj{"type": "STEP_STARTED", "stepName": name})
	case chatstream.VerbStepFinish:
		name, ok := e.steps[ev.StepID]
		if !ok {
			return sink.OutOfOrder(ev, "step is not open")
		}
		delete(e.steps, ev.StepID)
		e.stepOrder = removeString(e.stepOrder, ev.StepID)
		*out = append(*out, obj{"type": "STEP_FINISHED", "stepName": name})
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
			*out = append(*out, obj{"type": "RAW", "event": ev.Raw.Payload, "source": ev.Raw.Dialect})
		}
	case chatstream.VerbGap:
		*out = append(*out, obj{"type": "CUSTOM", "name": "chatstream.gap", "value": obj{"from": ev.From, "to": ev.To, "reason": ev.Reason}})
	case chatstream.VerbRunFinish:
		if ev.Usage != nil {
			e.foldUsage(ev)
		}
		e.closeAll(out)
		o := obj{"type": "RUN_FINISHED", "threadId": e.thread(), "runId": e.runID}
		switch {
		case ev.Finish() == chatstream.FinishCancelled:
			o["outcome"] = obj{"type": outcomeCancel}
		case len(e.interrupts) > 0:
			o["outcome"] = obj{"type": "interrupt", "interrupts": e.interruptJSON()}
		}
		e.addUsage(o)
		*out = append(*out, o)
	case chatstream.VerbRunError:
		if ev.Usage != nil {
			e.foldUsage(ev)
		}
		e.closeAll(out)
		o := obj{"type": "RUN_ERROR", "message": firstNonEmpty(ev.Message, ev.Code, "the run failed")}
		if ev.Code != "" {
			o["code"] = ev.Code
		}
		e.addUsage(o)
		*out = append(*out, o)
	case chatstream.VerbRunAbort:
		e.closeAll(out)
		o := obj{"type": "RUN_FINISHED", "threadId": e.thread(), "runId": e.runID, "outcome": obj{"type": outcomeCancel}}
		e.addUsage(o)
		*out = append(*out, o)
	}
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

func removeString(ss []string, s string) []string {
	for i, v := range ss {
		if v == s {
			return append(ss[:i], ss[i+1:]...)
		}
	}
	return ss
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

// addUsage adds the run's usage, once, in AG-UI's TokenUsage shape: inputTokens
// every prompt token, outputTokens every generated token, and reasoning, cache
// read and cache write as parts of those. Counts that are zero are omitted:
// AG-UI's absent means "not reported".
func (e *encoder) addUsage(o obj) {
	if e.usage == nil {
		return
	}
	u := *e.usage
	t := obj{"inputTokens": u.Input(), "outputTokens": u.Generated(), "totalTokens": u.Total()}
	if e.provider != "" {
		t["provider"] = e.provider
	}
	if e.model != "" {
		t["model"] = e.model
	}
	if u.Reasoning > 0 {
		t["reasoningTokens"] = u.Reasoning
	}
	if u.CacheRead > 0 {
		t["cachedInputTokens"] = u.CacheRead
	}
	if u.CacheWrite > 0 {
		t["cacheWriteInputTokens"] = u.CacheWrite
	}
	o["usage"] = []obj{t}
}

func (e *encoder) partStart(out *[]obj, ev chatstream.Event) error {
	if _, open := e.parts[ev.PartID]; open {
		return sink.OutOfOrder(ev, "part is already open")
	}
	p := &partState{kind: ev.PartKind(), meta: ev}
	e.parts[ev.PartID] = p
	e.order = append(e.order, ev.PartID)
	switch p.kind {
	case chatstream.PartText, chatstream.PartRefusal:
		*out = append(*out, obj{"type": "TEXT_MESSAGE_START", "messageId": ev.PartID, "role": "assistant"})
	case chatstream.PartReasoning:
		*out = append(*out,
			obj{"type": "REASONING_START", "messageId": ev.PartID},
			obj{"type": "REASONING_MESSAGE_START", "messageId": ev.PartID, "role": "reasoning"})
	case chatstream.PartToolCall:
		name := sink.MetaString(ev, sink.MetaName)
		if name == "" {
			name = "tool"
		}
		*out = append(*out, obj{"type": "TOOL_CALL_START", "toolCallId": ev.PartID, "toolCallName": name})
	default: // tool_result, source, file and data open nothing on the wire until they end
	}
	return nil
}

func (e *encoder) partDelta(out *[]obj, ev chatstream.Event) error {
	p, ok := e.parts[ev.PartID]
	if !ok {
		return sink.OutOfOrder(ev, "part is not open")
	}
	switch p.kind {
	case chatstream.PartText, chatstream.PartRefusal:
		// AG-UI requires a non-empty delta.
		if ev.Text != "" {
			*out = append(*out, obj{"type": "TEXT_MESSAGE_CONTENT", "messageId": ev.PartID, "delta": ev.Text})
		}
	case chatstream.PartReasoning:
		if ev.Text != "" {
			*out = append(*out, obj{"type": "REASONING_MESSAGE_CONTENT", "messageId": ev.PartID, "delta": ev.Text})
		}
	case chatstream.PartToolCall:
		p.streamed = p.streamed || ev.JSONFragment != ""
		*out = append(*out, obj{"type": "TOOL_CALL_ARGS", "toolCallId": ev.PartID, "delta": ev.JSONFragment})
	default:
		p.text.WriteString(ev.Text)
		p.text.WriteString(ev.JSONFragment)
	}
	return nil
}

func (e *encoder) partEnd(out *[]obj, ev chatstream.Event) error {
	p, ok := e.parts[ev.PartID]
	if !ok {
		return sink.OutOfOrder(ev, "part is not open")
	}
	delete(e.parts, ev.PartID)
	e.order = removeString(e.order, ev.PartID)
	e.closePart(out, ev.PartID, p, ev.Final)
	return nil
}

func (e *encoder) closePart(out *[]obj, id string, p *partState, final json.RawMessage) {
	switch p.kind {
	case chatstream.PartText, chatstream.PartRefusal:
		*out = append(*out, obj{"type": "TEXT_MESSAGE_END", "messageId": id})
	case chatstream.PartReasoning:
		*out = append(*out,
			obj{"type": "REASONING_MESSAGE_END", "messageId": id},
			obj{"type": "REASONING_END", "messageId": id})
		if len(final) > 0 {
			*out = append(*out, obj{"type": "REASONING_ENCRYPTED_VALUE", "subtype": "message", "entityId": id, "encryptedValue": rawText(final)})
		}
	case chatstream.PartToolCall:
		if len(final) > 0 && !p.streamed {
			*out = append(*out, obj{"type": "TOOL_CALL_ARGS", "toolCallId": id, "delta": string(final)})
		}
		*out = append(*out, obj{"type": "TOOL_CALL_END", "toolCallId": id})
	case chatstream.PartToolResult:
		content := p.text.String()
		if content == "" && len(final) > 0 {
			content = rawText(final)
		}
		callID := sink.MetaString(p.meta, sink.MetaCallID)
		if callID == "" {
			callID = id
		}
		*out = append(*out, obj{"type": "TOOL_CALL_RESULT", "messageId": id, "toolCallId": callID, "content": content, "role": "tool"})
	default: // source, file and data parts have no AG-UI event
	}
}

// rawText is a JSON string's value, or the JSON text itself for anything else.
func rawText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// closeAll closes everything still open before RUN_FINISHED: parts newest first,
// then steps.
func (e *encoder) closeAll(out *[]obj) {
	for len(e.order) > 0 {
		id := e.order[len(e.order)-1]
		e.order = e.order[:len(e.order)-1]
		p, ok := e.parts[id]
		delete(e.parts, id)
		if ok {
			e.closePart(out, id, p, nil)
		}
	}
	for len(e.stepOrder) > 0 {
		id := e.stepOrder[len(e.stepOrder)-1]
		e.stepOrder = e.stepOrder[:len(e.stepOrder)-1]
		*out = append(*out, obj{"type": "STEP_FINISHED", "stepName": e.steps[id]})
		delete(e.steps, id)
	}
}

func (e *encoder) approval(out *[]obj, ev chatstream.Event) {
	if ev.Mode == chatstream.ApprovalSuspend {
		in := interrupt{id: ev.ApprovalID, reason: firstNonEmpty(ev.Reason, "approval"), callID: ev.CallID}
		if ev.ExpiresAt != nil {
			s := ev.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			in.expires = &s
		}
		e.interrupts = append(e.interrupts, in)
		return
	}
	v := obj{"approvalId": ev.ApprovalID, "reason": ev.Reason}
	if ev.CallID != "" {
		v["toolCallId"] = ev.CallID
	}
	if len(ev.Descriptor) > 0 {
		v["descriptor"] = ev.Descriptor
	}
	if ev.ExpiresAt != nil {
		v["expiresAt"] = ev.ExpiresAt
	}
	*out = append(*out, obj{"type": "CUSTOM", "name": "chatstream.approval_request", "value": v})
}

func (e *encoder) interruptJSON() []obj {
	var out []obj
	for _, in := range e.interrupts {
		o := obj{"id": in.id, "reason": in.reason}
		if in.callID != "" {
			o["toolCallId"] = in.callID
		}
		if in.expires != nil {
			o["expiresAt"] = *in.expires
		}
		out = append(out, o)
	}
	return out
}

func (e *encoder) activity(out *[]obj, ev chatstream.Event) {
	switch ev.Kind {
	case sink.ActivityStateSnapshot:
		*out = append(*out, obj{"type": "STATE_SNAPSHOT", "snapshot": nullable(ev.Value)})
		return
	case sink.ActivityStateDelta:
		*out = append(*out, obj{"type": "STATE_DELTA", "delta": nullable(ev.Patch)})
		return
	case sink.ActivityReplaceContent:
		*out = append(*out, obj{"type": "CUSTOM", "name": sink.ActivityReplaceContent, "value": nullable(ev.Value)})
		return
	}
	if e.activities == nil {
		e.activities = map[string]bool{}
	}
	mid := "activity:" + ev.Kind
	if len(ev.Value) > 0 || !e.activities[ev.Kind] {
		e.activities[ev.Kind] = true
		*out = append(*out, obj{"type": "ACTIVITY_SNAPSHOT", "messageId": mid, "activityType": ev.Kind, "content": contentObject(ev.Value), "replace": true})
		if len(ev.Patch) == 0 {
			return
		}
	}
	if len(ev.Patch) > 0 {
		*out = append(*out, obj{"type": "ACTIVITY_DELTA", "messageId": mid, "activityType": ev.Kind, "patch": ev.Patch})
	}
}

func nullable(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// contentObject is v when it is a JSON object, else {"value": v}: ACTIVITY_SNAPSHOT
// content must be an object.
func contentObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return obj{}
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && m != nil {
		return raw
	}
	return obj{"value": raw}
}

// Close writes nothing when the run ended. Otherwise it closes what is open and
// writes RUN_ERROR (code chatstream.CodeStreamLost), because AG-UI requires a
// terminal signal distinguishable from truncation.
func (e *encoder) Close(w sink.Writer, cause error) error {
	already, ended := e.Closing()
	if already || ended {
		return nil
	}
	var out []obj
	e.closeAll(&out)
	out = append(out, obj{"type": "RUN_ERROR", "message": sink.CauseText(cause), "code": chatstream.CodeStreamLost})
	for _, o := range out {
		if err := e.emit(w, "", o); err != nil {
			return err
		}
	}
	return nil
}
