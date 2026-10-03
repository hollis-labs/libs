//nolint:misspell // "cancelled" is ACP's wire value for a stopReason
package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/internal/decodekit"
)

// stopReasons maps ACP's stopReason to a chatstream finish reason. "cancelled"
// is not a finish: it is a run.abort (see terminal).
var stopReasons = map[string]chatstream.FinishReason{
	"end_turn":          chatstream.FinishStop,
	"max_tokens":        chatstream.FinishLength,
	"max_turn_requests": chatstream.FinishTurnLimit,
	"refusal":           chatstream.FinishRefusal,
}

// errorCodeCancelled is JSON-RPC "request cancelled".
const errorCodeCancelled = -32800

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	// Error is read leniently (see readError): a response whose error has a
	// string code or a structured message is still the run's terminal frame.
	Error json.RawMessage `json:"error"`
}

type update struct {
	SessionUpdate string          `json:"sessionUpdate"`
	MessageID     string          `json:"messageId"`
	Content       json.RawMessage `json:"content"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	RawInput      json.RawMessage `json:"rawInput"`
	RawOutput     json.RawMessage `json:"rawOutput"`
	ToolContent   json.RawMessage `json:"-"`
}

type chunkState struct {
	kind   chatstream.PartKind // "" when no chunk part is open
	partID string
	msgID  string // the messageId of the open chunk run, "" if the agent sent none
}

type toolState struct {
	argsOpen bool // part.start sent, part.end not yet
	resulted bool
}

type decoder struct {
	*decodekit.Base
	sessionID string
	cur       chunkState
	msgOpen   bool
	msgID     string
	msgSynth  bool   // the open message's id was made up here, not sent by the agent
	openTool  string // id of a tool_call part whose arguments are still awaited
	msgSeq    int
	partSeq   int
	tools     map[string]*toolState
}

func newDecoder(o chatstream.DecodeOptions) *decoder {
	return &decoder{Base: decodekit.New(o), tools: map[string]*toolState{}}
}

// Close ends the stream; see decodekit.Base.Close.
func (d *decoder) Close(cause error) []chatstream.Event { return d.Base.Close(cause) }

func (d *decoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	if d.Terminated() {
		return nil, nil
	}
	var m message
	if !parses(f.Data, &m) || (m.JSONRPC == "" && m.Method == "" && m.Result == nil && !present(m.Error)) {
		return d.raw(d.start(), "malformed", f.Data), nil
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method == "session/update" && !hasID:
		return d.sessionUpdate(m), nil
	case m.Method == "session/request_permission" && hasID:
		return d.permission(m), nil
	case m.Method != "":
		return d.raw(d.start(), m.Method, f.Data), nil
	case present(m.Error):
		return d.errorResponse(m), nil
	case len(m.Result) > 0:
		return d.response(m, f.Data), nil
	}
	return d.raw(d.start(), "unclassified", f.Data), nil
}

func (d *decoder) raw(out []chatstream.Event, typ string, payload []byte) []chatstream.Event {
	return d.Emit(out, d.RawEvent(DialectName, typ, payload))
}

// start emits run.start once, with the session id in Ext when it is known.
func (d *decoder) start() []chatstream.Event {
	if d.Started() {
		return nil
	}
	ev := d.Event(chatstream.VerbRunStart)
	ev.Provider, ev.Model = d.Options().Provider, d.Options().Model
	if d.sessionID != "" {
		ev.Ext = map[string]json.RawMessage{DialectName: mustJSON(map[string]string{"session_id": d.sessionID})}
	}
	return d.Emit(nil, ev)
}

func (d *decoder) learnSession(id string) {
	if d.sessionID == "" {
		d.sessionID = id
	}
}

func (d *decoder) closeChunk(out []chatstream.Event) []chatstream.Event {
	if d.cur.kind == "" {
		return out
	}
	ev := d.Event(chatstream.VerbPartEnd)
	ev.PartID = d.cur.partID
	d.cur = chunkState{}
	return d.Emit(out, ev)
}

// closeOpenTool ends a tool_call part still waiting for arguments. Parts do not
// interleave: the wait lasts only until the next update that is not about that
// tool call.
func (d *decoder) closeOpenTool(out []chatstream.Event) []chatstream.Event {
	if d.openTool == "" {
		return out
	}
	id := d.openTool
	d.openTool = ""
	d.tools[id].argsOpen = false
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID = id
	return d.Emit(out, en)
}

func (d *decoder) closeMessage(out []chatstream.Event) []chatstream.Event {
	out = d.closeChunk(out)
	if !d.msgOpen {
		return out
	}
	ev := d.Event(chatstream.VerbMessageEnd)
	ev.MessageID = d.msgID
	d.msgOpen = false
	return d.Emit(out, ev)
}

func (d *decoder) openMessage(out []chatstream.Event, id string) []chatstream.Event {
	if d.msgOpen {
		return out
	}
	d.msgSeq++
	synth := id == ""
	if synth {
		id = fmt.Sprintf("msg-%d", d.msgSeq)
	}
	d.msgOpen, d.msgID, d.msgSynth = true, id, synth
	ev := d.Event(chatstream.VerbMessageStart)
	ev.MessageID, ev.Role = id, "assistant"
	return d.Emit(out, ev)
}

func (d *decoder) newPartID(prefix string) string {
	d.partSeq++
	return fmt.Sprintf("%s-%d", prefix, d.partSeq)
}

func (d *decoder) sessionUpdate(m message) []chatstream.Event {
	var p struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || len(p.Update) == 0 {
		return d.raw(d.start(), "session/update", mustMarshalMsg(m))
	}
	d.learnSession(p.SessionID)
	var u update
	if err := json.Unmarshal(p.Update, &u); err != nil {
		return d.raw(d.start(), "session/update", p.Update)
	}
	out := d.start()
	if u.SessionUpdate != "tool_call_update" || u.ToolCallID != d.openTool {
		out = d.closeOpenTool(out)
	}
	switch u.SessionUpdate {
	case "agent_message_chunk":
		return d.chunk(out, u, p.Update, chatstream.PartText, "agent_message_chunk")
	case "agent_thought_chunk":
		return d.chunk(out, u, p.Update, chatstream.PartReasoning, "agent_thought_chunk")
	}
	// anything else ends a run of chunks
	out = d.closeChunk(out)
	switch u.SessionUpdate {
	case "user_message_chunk":
		return d.raw(out, u.SessionUpdate, p.Update)
	case "tool_call":
		return d.toolCall(out, u, p.Update)
	case "tool_call_update":
		return d.toolCallUpdate(out, u, p.Update)
	case "plan":
		return d.activity(out, "acp.plan", p.Update)
	case "available_commands_update":
		return d.activity(out, "acp.available_commands", p.Update)
	case "current_mode_update":
		return d.activity(out, "acp.current_mode", p.Update)
	case "config_option_update":
		return d.activity(out, "acp.config_options", p.Update)
	case "session_info_update":
		return d.activity(out, "acp.session_info", p.Update)
	case "usage_update":
		return d.activity(out, "acp.usage", p.Update)
	}
	return d.raw(out, "session/update."+u.SessionUpdate, p.Update)
}

func (d *decoder) activity(out []chatstream.Event, kind string, value []byte) []chatstream.Event {
	ev := d.Event(chatstream.VerbActivity)
	ev.Kind, ev.Value = kind, compact(value)
	return d.Emit(out, ev)
}

func (d *decoder) chunk(out []chatstream.Event, u update, whole []byte, kind chatstream.PartKind, name string) []chatstream.Event {
	var c struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(u.Content, &c); err != nil || c.Type != "text" {
		// image, audio, resource...: kept as raw, and it does not break the run
		return d.raw(out, name+"."+orUnknown(c.Type), whole)
	}
	if d.cur.kind != "" && (d.cur.kind != kind || (u.MessageID != "" && d.cur.msgID != "" && u.MessageID != d.cur.msgID)) {
		out = d.closeChunk(out)
	}
	if d.msgOpen && u.MessageID != "" && d.msgID != u.MessageID && d.cur.kind == "" && !d.msgSynth {
		out = d.closeMessage(out)
	}
	out = d.openMessage(out, u.MessageID)
	if d.cur.kind == "" {
		id := d.newPartID(string(kind))
		st := d.Event(chatstream.VerbPartStart)
		st.PartID, st.Kind = id, string(kind)
		out = d.Emit(out, st)
		d.cur = chunkState{kind: kind, partID: id, msgID: u.MessageID}
	}
	if c.Text != "" {
		dl := d.Event(chatstream.VerbPartDelta)
		dl.PartID, dl.Text = d.cur.partID, c.Text
		out = d.Emit(out, dl)
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// toolMeta is the part.start Meta of a tool_call part. The tool's name is the
// update's name, else its title, else its kind ("tool" if it has none of them);
// detail is the command, path or query in rawInput when there is one.
func (d *decoder) toolMeta(u update) map[string]json.RawMessage {
	name := firstNonEmpty(u.Name, u.Title, u.Kind, "tool")
	meta := map[string]json.RawMessage{chatstream.MetaName: mustJSON(name), "tool_call_id": mustJSON(u.ToolCallID)}
	if detail := inputDetail(u.RawInput); detail != "" {
		meta[chatstream.MetaDetail] = mustJSON(detail)
	}
	for k, v := range map[string]string{"title": u.Title, "kind": u.Kind, "status": u.Status} {
		if v != "" {
			meta[k] = mustJSON(v)
		}
	}
	return meta
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// inputDetail picks a short label out of a tool's arguments: the first of the
// usual keys that holds a string.
func inputDetail(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if !parses(raw, &m) {
		return ""
	}
	for _, k := range []string{"command", "cmd", "path", "file_path", "filePath", "query", "pattern", "url"} {
		var s string
		if v, ok := m[k]; ok && parses(v, &s) && s != "" {
			return s
		}
	}
	return ""
}

func (d *decoder) toolCall(out []chatstream.Event, u update, whole []byte) []chatstream.Event {
	if u.ToolCallID == "" {
		return d.raw(out, "tool_call", whole)
	}
	if _, dup := d.tools[u.ToolCallID]; dup {
		return d.toolCallUpdate(out, u, whole)
	}
	return d.ensureTool(out, u)
}

// ensureTool opens the tool_call part for u's id (inside the message) and, if
// the update carries rawInput, sends the arguments and closes it.
func (d *decoder) ensureTool(out []chatstream.Event, u update) []chatstream.Event {
	if _, ok := d.tools[u.ToolCallID]; !ok {
		out = d.openMessage(out, "")
		st := d.Event(chatstream.VerbPartStart)
		st.PartID, st.Kind, st.Meta = u.ToolCallID, string(chatstream.PartToolCall), d.toolMeta(u)
		out = d.Emit(out, st)
		d.tools[u.ToolCallID] = &toolState{argsOpen: true}
		d.openTool = u.ToolCallID
	}
	return d.sendArgs(out, u)
}

func (d *decoder) sendArgs(out []chatstream.Event, u update) []chatstream.Event {
	t := d.tools[u.ToolCallID]
	if !t.argsOpen || !present(u.RawInput) {
		return out
	}
	args := compact(u.RawInput)
	dl := d.Event(chatstream.VerbPartDelta)
	dl.PartID, dl.JSONFragment = u.ToolCallID, string(args)
	out = d.Emit(out, dl)
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID, en.Final = u.ToolCallID, args
	t.argsOpen = false
	d.openTool = ""
	return d.Emit(out, en)
}

// toolCallUpdate: rawInput that arrives late completes the call's arguments; a
// completed or failed status yields one tool_result part (after the call's part
// is closed, with or without arguments); other updates are activities.
func (d *decoder) toolCallUpdate(out []chatstream.Event, u update, whole []byte) []chatstream.Event {
	if u.ToolCallID == "" {
		return d.raw(out, "tool_call_update", whole)
	}
	out = d.ensureTool(out, u)
	t := d.tools[u.ToolCallID]
	if u.Status != "completed" && u.Status != "failed" {
		return d.activity(out, "acp.tool_call_update", whole)
	}
	if t.argsOpen { // finished without ever sending arguments
		en := d.Event(chatstream.VerbPartEnd)
		en.PartID = u.ToolCallID
		out = d.Emit(out, en)
		t.argsOpen = false
		d.openTool = ""
	}
	if t.resulted {
		return d.raw(out, "tool_call_update", whole)
	}
	t.resulted = true
	var full struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(whole, &full)
	final := map[string]json.RawMessage{"status": mustJSON(u.Status)}
	if present(full.Content) {
		final["content"] = compact(full.Content)
	}
	if present(u.RawOutput) {
		final["raw_output"] = compact(u.RawOutput)
	}
	id := u.ToolCallID + "#result"
	st := d.Event(chatstream.VerbPartStart)
	st.PartID, st.Kind = id, string(chatstream.PartToolResult)
	st.Meta = map[string]json.RawMessage{chatstream.MetaCallID: mustJSON(u.ToolCallID), chatstream.MetaIsError: mustJSON(u.Status == "failed")}
	out = d.Emit(out, st)
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID, en.Final = id, mustJSON(final)
	return d.Emit(out, en)
}

func (d *decoder) permission(m message) []chatstream.Event {
	var p struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
		} `json:"toolCall"`
	}
	_ = json.Unmarshal(m.Params, &p)
	d.learnSession(p.SessionID)
	out := d.closeOpenTool(d.closeChunk(d.start()))
	ev := d.Event(chatstream.VerbApprovalRequest)
	ev.ApprovalID = idString(m.ID)
	ev.CallID, ev.Reason = p.ToolCall.ToolCallID, p.ToolCall.Title
	ev.Descriptor, ev.Mode = compact(m.Params), chatstream.ApprovalInBand
	return d.Emit(out, ev)
}

// response handles a JSON-RPC result. A stopReason is the turn's end.
func (d *decoder) response(m message, whole []byte) []chatstream.Event {
	var r struct {
		StopReason string          `json:"stopReason"`
		SessionID  string          `json:"sessionId"`
		Usage      json.RawMessage `json:"usage"`
	}
	_ = json.Unmarshal(m.Result, &r)
	d.learnSession(r.SessionID)
	if r.StopReason == "" {
		return d.raw(d.start(), "response", whole)
	}
	out := d.start()
	out = d.closeMessage(d.closeOpenTool(out))
	out = d.Unwind(out)
	if r.StopReason == "cancelled" {
		ev := d.Event(chatstream.VerbRunAbort)
		ev.Reason = "cancelled"
		return d.Emit(out, ev)
	}
	ev := d.Event(chatstream.VerbRunFinish)
	reason, known := stopReasons[r.StopReason]
	if !known {
		reason = chatstream.FinishOther
	}
	ev.Reason, ev.RawReason = string(reason), r.StopReason
	if u, ok := endTurnUsage(r.Usage); ok {
		ev.Usage = &u
	}
	return d.Emit(out, ev)
}

// endTurnUsage reads the draft end-of-turn usage (RFD, not in ACP v1) with the
// inclusive convention: inputTokens contains the cached counts and outputTokens
// contains thoughtTokens. Counts that contradict that are not reported.
func endTurnUsage(raw json.RawMessage) (chatstream.Usage, bool) {
	if !present(raw) {
		return chatstream.Usage{}, false
	}
	var u struct {
		Input   int `json:"inputTokens"`
		Output  int `json:"outputTokens"`
		Thought int `json:"thoughtTokens"`
		Read    int `json:"cachedReadTokens"`
		Write   int `json:"cachedWriteTokens"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return chatstream.Usage{}, false
	}
	usage, err := chatstream.UsageFromInclusive(chatstream.UsageFinal, u.Input, u.Read, u.Write, u.Output, u.Thought)
	return usage, err == nil
}

func (d *decoder) errorResponse(m message) []chatstream.Event {
	out := d.closeMessage(d.closeOpenTool(d.start()))
	out = d.Unwind(out)
	code, msg, data := readError(m.Error)
	if code == strconv.Itoa(errorCodeCancelled) {
		ev := d.Event(chatstream.VerbRunAbort)
		ev.Reason = "cancelled"
		return d.Emit(out, ev)
	}
	ev := d.Event(chatstream.VerbRunError)
	ev.Code, ev.Message = code, msg
	if present(data) {
		ev.Ext = map[string]json.RawMessage{DialectName: mustJSON(map[string]json.RawMessage{"data": compact(data)})}
	}
	return d.Emit(out, ev)
}

// readError reads a JSON-RPC error object whatever its field types: the code as
// a number (1.0 is 1) or a string, the message as a string or as any JSON (its
// text). A bare string or other JSON in place of the object is the message. The
// code is "" only when the error carries none, and then ev.Code falls back to
// chatstream.CodeUpstreamError.
func readError(raw json.RawMessage) (code, msg string, data json.RawMessage) {
	var o struct {
		Code    json.RawMessage `json:"code"`
		Message json.RawMessage `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return chatstream.CodeUpstreamError, textOf(raw), nil
	}
	switch {
	case !present(o.Code):
		code = chatstream.CodeUpstreamError
	default:
		var f float64
		if json.Unmarshal(o.Code, &f) == nil && f == float64(int64(f)) {
			code = strconv.FormatInt(int64(f), 10)
		} else {
			code = textOf(o.Code)
		}
	}
	return code, textOf(o.Message), o.Data
}

// textOf is a JSON string's value, else the JSON's own text ("" for absent).
func textOf(raw json.RawMessage) string {
	if !present(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(compact(raw))
}

func present(r json.RawMessage) bool {
	s := bytes.TrimSpace(r)
	return len(s) > 0 && string(s) != "null"
}

func compact(b []byte) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return mustJSON(string(b))
	}
	return buf.Bytes()
}

func idString(id json.RawMessage) string {
	var s string
	if json.Unmarshal(id, &s) == nil {
		return s
	}
	return string(bytes.TrimSpace(id))
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}

func mustMarshalMsg(m message) []byte { return mustJSON(m) }

// parses reports whether data is JSON that fits v.
func parses(data []byte, v any) bool { return json.Unmarshal(data, v) == nil }
