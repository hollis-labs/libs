package codexappserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/internal/decodekit"
)

// envelope is one JSON-RPC message: a notification (method, no id), a server
// request (method and id) or a response (id, result or error). Codex leaves out
// the "jsonrpc" member; it is not read.
type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// common holds the fields most notifications share.
type common struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
	Index    *int   `json:"summaryIndex"`
}

type turn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
}

type turnError struct {
	Message           json.RawMessage `json:"message"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
	AdditionalDetails json.RawMessage `json:"additionalDetails"`
}

type tokenUsage struct {
	Total *breakdown `json:"total"`
	Last  *breakdown `json:"last"`
}

type breakdown struct {
	Input      int `json:"inputTokens"`
	Cached     int `json:"cachedInputTokens"`
	CacheWrite int `json:"cacheWriteInputTokens"`
	Output     int `json:"outputTokens"`
	Reasoning  int `json:"reasoningOutputTokens"`
}

// item is a ThreadItem, every variant's fields in one struct.
type item struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`

	// agentMessage
	Text  *string `json:"text"`
	Phase string  `json:"phase"`

	// reasoning
	Summary json.RawMessage `json:"summary"` // []string; userMessage's content is objects
	Content json.RawMessage `json:"content"`

	// commandExecution
	Command          string          `json:"command"`
	Cwd              string          `json:"cwd"`
	CommandActions   []commandAction `json:"commandActions"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	DurationMS       *int64          `json:"durationMs"`

	// fileChange
	Changes json.RawMessage `json:"changes"`

	// mcpToolCall, dynamicToolCall, collabAgentToolCall
	Server       string          `json:"server"`
	Namespace    string          `json:"namespace"`
	Tool         string          `json:"tool"`
	Arguments    json.RawMessage `json:"arguments"`
	Result       json.RawMessage `json:"result"`
	Error        json.RawMessage `json:"error"`
	ContentItems json.RawMessage `json:"contentItems"`
	Success      *bool           `json:"success"`
	Prompt       *string         `json:"prompt"`
	Model        *string         `json:"model"`
	Sender       string          `json:"senderThreadId"`
	Receivers    json.RawMessage `json:"receiverThreadIds"`
	AgentsStates json.RawMessage `json:"agentsStates"`

	// webSearch
	Query   string          `json:"query"`
	Action  json.RawMessage `json:"action"`
	Results json.RawMessage `json:"results"`
}

type commandAction struct {
	Command string `json:"command"`
}

type itemState struct {
	kind     chatstream.PartKind // of the text/reasoning part
	open     bool                // the text/reasoning part is open
	ended    bool                // the text/reasoning part was ended
	sent     string              // text sent as deltas
	index    int                 // the reasoning summary index last streamed
	phase    string              // an agentMessage's phase, from item/started
	called   bool
	resulted bool
}

type decoder struct {
	*decodekit.Base
	threadID string
	turnID   string
	turned   bool
	items    map[string]*itemState
	open     []string // ids of open text/reasoning parts, oldest first
	msgOpen  bool
	usage    *usageTrack
}

// usageTrack derives the turn's usage from the thread's running totals: the
// total before the turn's first model call (its total minus its last) and the
// latest total.
type usageTrack struct {
	before, latest breakdown
}

func newDecoder(o chatstream.DecodeOptions) *decoder {
	return &decoder{Base: decodekit.New(o), items: map[string]*itemState{}}
}

// Close ends the stream. A stream that ends without turn/completed closes its
// open parts, each with the text it streamed in final, and ends with run.error
// upstream_truncated; see decodekit.Base.Close.
func (d *decoder) Close(cause error) []chatstream.Event {
	if d.Closed() || d.Terminated() {
		return d.Base.Close(cause)
	}
	out := d.endOpen(nil)
	return append(out, d.Base.Close(cause)...)
}

func (d *decoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	if d.Terminated() {
		return nil, nil
	}
	env, ok := readEnvelope(f.Data)
	if !ok {
		return d.raw(d.start("", ""), "malformed", f.Data), nil
	}
	switch {
	case env.Method == "" && present(env.ID):
		return d.raw(d.start("", ""), "response", f.Data), nil
	case env.Method == "":
		return d.raw(d.start("", ""), "malformed", f.Data), nil
	case present(env.ID):
		return d.serverRequest(env, f.Data), nil
	}
	var c common
	_ = json.Unmarshal(env.Params, &c)
	if c.ThreadID == "" && env.Method == "thread/started" {
		c.ThreadID = startedThread(env.Params)
	}
	if c.ThreadID != "" && d.threadID != "" && c.ThreadID != d.threadID {
		return d.raw(d.start("", ""), env.Method, f.Data), nil // another thread's traffic
	}
	switch env.Method {
	case "thread/started":
		if d.Started() {
			return d.raw(nil, env.Method, f.Data), nil
		}
		return d.start(c.ThreadID, ""), nil
	case "turn/started":
		return d.turnStarted(env.Params, c, f.Data), nil
	case "item/started", "item/completed":
		return d.itemEvent(env, f.Data), nil
	case "item/agentMessage/delta":
		return d.delta(chatstream.PartText, c, f.Data, env.Method), nil
	case "item/reasoning/summaryTextDelta":
		return d.delta(chatstream.PartReasoning, c, f.Data, env.Method), nil
	case "item/reasoning/summaryPartAdded":
		return d.start(c.ThreadID, ""), nil // the next summaryTextDelta's index says it
	case "thread/tokenUsage/updated":
		return d.tokenUsage(env.Params, c, f.Data), nil
	case "turn/plan/updated":
		return d.activity(d.start(c.ThreadID, ""), "codex.plan", pick(env.Params, "explanation", "plan")), nil
	case "turn/diff/updated":
		return d.activity(d.start(c.ThreadID, ""), "codex.diff", pick(env.Params, "diff")), nil
	case "error":
		return d.errorNotice(env.Params, c), nil
	case "warning":
		return d.activity(d.start(c.ThreadID, ""), "codex.warning", pick(env.Params, "message")), nil
	case "serverRequest/resolved":
		var r struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		_ = json.Unmarshal(env.Params, &r)
		return d.activity(d.start(c.ThreadID, ""), "codex.server_request_resolved",
			mustJSON(map[string]json.RawMessage{"request_id": orNull(r.RequestID)})), nil
	case "turn/completed":
		return d.turnCompleted(env.Params), nil
	}
	return d.raw(d.start(c.ThreadID, ""), env.Method, f.Data), nil
}

// start emits run.start once. The thread id, when known, goes in Ext and
// becomes the run id if none was configured; the turn id is preferred.
func (d *decoder) start(threadID, turnID string) []chatstream.Event {
	if d.threadID == "" && threadID != "" {
		d.threadID = threadID
	}
	if d.Started() {
		return nil
	}
	d.SetRunID(firstNonEmpty(turnID, threadID))
	ev := d.Event(chatstream.VerbRunStart)
	ev.Provider, ev.Model = d.Options().Provider, d.Options().Model
	if ext := ids(threadID, turnID); ext != nil {
		ev.Ext = map[string]json.RawMessage{extKey: ext}
	}
	return d.Emit(nil, ev)
}

func ids(threadID, turnID string) json.RawMessage {
	m := map[string]string{}
	if threadID != "" {
		m["thread_id"] = threadID
	}
	if turnID != "" {
		m["turn_id"] = turnID
	}
	if len(m) == 0 {
		return nil
	}
	return mustJSON(m)
}

func startedThread(params json.RawMessage) string {
	var p struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(params, &p)
	return p.Thread.ID
}

func (d *decoder) raw(out []chatstream.Event, typ string, payload []byte) []chatstream.Event {
	return d.Emit(out, d.RawEvent(extKey, typ, payload))
}

func (d *decoder) activity(out []chatstream.Event, kind string, value json.RawMessage) []chatstream.Event {
	ev := d.Event(chatstream.VerbActivity)
	ev.Kind, ev.Value = kind, value
	return d.Emit(out, ev)
}

func (d *decoder) turnStarted(params json.RawMessage, c common, whole []byte) []chatstream.Event {
	var p struct {
		Turn turn `json:"turn"`
	}
	_ = json.Unmarshal(params, &p)
	out := d.start(c.ThreadID, p.Turn.ID)
	if d.turned {
		return d.raw(out, "turn/started", whole) // one decoder is one turn
	}
	d.turned, d.turnID = true, p.Turn.ID
	ev := d.Event(chatstream.VerbStepStart)
	ev.StepID = firstNonEmpty(p.Turn.ID, "turn-1")
	if ext := ids(c.ThreadID, p.Turn.ID); ext != nil {
		ev.Ext = map[string]json.RawMessage{extKey: ext}
	}
	return d.Emit(out, ev)
}

// serverRequest reports a request codex sent the client. Approval and
// user-input requests become activity; the decoder does not answer them.
func (d *decoder) serverRequest(env envelope, whole []byte) []chatstream.Event {
	var c common
	_ = json.Unmarshal(env.Params, &c)
	out := d.start(c.ThreadID, "")
	kind := ""
	switch env.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval",
		"applyPatchApproval", "execCommandApproval":
		kind = "codex.approval_request"
	case "item/tool/requestUserInput", "mcpServer/elicitation/request":
		kind = "codex.user_input_request"
	default:
		return d.raw(out, env.Method, whole)
	}
	v := map[string]json.RawMessage{"request_id": compact(env.ID), "method": mustJSON(env.Method), "params": orNull(env.Params)}
	if c.ItemID != "" {
		v["item_id"] = mustJSON(c.ItemID)
	}
	return d.activity(out, kind, mustJSON(v))
}

func (d *decoder) errorNotice(params json.RawMessage, c common) []chatstream.Event {
	var p struct {
		Error     turnError `json:"error"`
		WillRetry bool      `json:"willRetry"`
	}
	_ = json.Unmarshal(params, &p)
	v := map[string]json.RawMessage{
		"message":    mustJSON(errorMessage(p.Error.Message)),
		"will_retry": mustJSON(p.WillRetry),
	}
	if present(p.Error.CodexErrorInfo) {
		v["codex_error_info"] = compact(p.Error.CodexErrorInfo)
	}
	if present(p.Error.AdditionalDetails) {
		v["additional_details"] = compact(p.Error.AdditionalDetails)
	}
	return d.activity(d.start(c.ThreadID, ""), "codex.error", mustJSON(v))
}

func (d *decoder) message(out []chatstream.Event) []chatstream.Event {
	if d.msgOpen {
		return out
	}
	d.msgOpen = true
	ev := d.Event(chatstream.VerbMessageStart)
	ev.MessageID, ev.Role = "msg-1", "assistant"
	return d.Emit(out, ev)
}

func (d *decoder) state(id string) *itemState {
	st := d.items[id]
	if st == nil {
		st = &itemState{}
		d.items[id] = st
	}
	return st
}

// openText opens the text or reasoning part of item id.
func (d *decoder) openText(out []chatstream.Event, id string, kind chatstream.PartKind, phase string) []chatstream.Event {
	st := d.state(id)
	out = d.message(out)
	ps := d.Event(chatstream.VerbPartStart)
	ps.PartID, ps.Kind = id, string(kind)
	if p := metaPhase(phase); p != "" {
		ps.Meta = map[string]json.RawMessage{chatstream.MetaPhase: mustJSON(p)}
	}
	st.kind, st.open = kind, true
	d.open = append(d.open, id)
	return d.Emit(out, ps)
}

// metaPhase maps codex's agentMessage phase onto chatstream's.
func metaPhase(phase string) string {
	switch phase {
	case "commentary":
		return "narration"
	case "final_answer":
		return "final"
	}
	return ""
}

func (d *decoder) sendText(out []chatstream.Event, id, text string) []chatstream.Event {
	if text == "" {
		return out
	}
	st := d.state(id)
	dl := d.Event(chatstream.VerbPartDelta)
	dl.PartID, dl.Text = id, text
	st.sent += text
	return d.Emit(out, dl)
}

// endText closes an open text or reasoning part with its complete text in final.
func (d *decoder) endText(out []chatstream.Event, id string, final json.RawMessage) []chatstream.Event {
	st := d.state(id)
	if !st.open {
		return out
	}
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID, en.Final = id, final
	st.open, st.ended = false, true
	for i, o := range d.open {
		if o == id {
			d.open = append(d.open[:i], d.open[i+1:]...)
			break
		}
	}
	return d.Emit(out, en)
}

// endOpen closes every open text and reasoning part with what it streamed.
func (d *decoder) endOpen(out []chatstream.Event) []chatstream.Event {
	for len(d.open) > 0 {
		id := d.open[len(d.open)-1]
		out = d.endText(out, id, mustJSON(map[string]string{"text": d.items[id].sent}))
	}
	return out
}

// delta streams an agentMessage or reasoning-summary delta, opening the part
// if item/started did not.
func (d *decoder) delta(kind chatstream.PartKind, c common, whole []byte, method string) []chatstream.Event {
	out := d.start(c.ThreadID, "")
	if c.ItemID == "" {
		return d.raw(out, method, whole)
	}
	st := d.state(c.ItemID)
	if st.ended || (st.open && st.kind != kind) {
		return d.raw(out, method, whole)
	}
	if !st.open {
		out = d.openText(out, c.ItemID, kind, st.phase)
	}
	if kind == chatstream.PartReasoning && c.Index != nil && *c.Index != st.index {
		if *c.Index > st.index && st.sent != "" {
			out = d.sendText(out, c.ItemID, "\n\n")
		}
		st.index = *c.Index
	}
	return d.sendText(out, c.ItemID, c.Delta)
}

func (d *decoder) itemEvent(env envelope, whole []byte) []chatstream.Event {
	var p struct {
		Item     json.RawMessage `json:"item"`
		ThreadID string          `json:"threadId"`
	}
	_ = json.Unmarshal(env.Params, &p)
	out := d.start(p.ThreadID, "")
	// A member whose type changed upstream is left zero; the rest still decodes.
	var it item
	_ = json.Unmarshal(p.Item, &it)
	if it.ID == "" {
		return d.raw(out, env.Method, whole)
	}
	done := env.Method == "item/completed"
	switch it.Type {
	case "agentMessage":
		text := ""
		if it.Text != nil {
			text = *it.Text
		}
		return d.text(out, chatstream.PartText, it, text, nil, done, whole, env.Method)
	case "reasoning":
		text, content := strings.Join(stringsOf(it.Summary), "\n\n"), strings.Join(stringsOf(it.Content), "\n\n")
		var extra map[string]string
		if content != "" {
			extra = map[string]string{"content": content}
			if text == "" {
				text = content
			}
		}
		return d.text(out, chatstream.PartReasoning, it, text, extra, done, whole, env.Method)
	case "commandExecution", "mcpToolCall", "fileChange", "webSearch", "dynamicToolCall", "collabAgentToolCall":
		return d.tool(out, it, d.state(it.ID), done)
	}
	return d.raw(out, env.Method+"."+it.Type, whole)
}

// text handles item/started and item/completed of an agentMessage or
// reasoning item. Its text streams as deltas; the completed item's text is
// the whole, and is what final carries.
func (d *decoder) text(out []chatstream.Event, kind chatstream.PartKind, it item, text string, extra map[string]string,
	done bool, whole []byte, method string) []chatstream.Event {
	st := d.state(it.ID)
	if st.ended || (st.open && st.kind != kind) {
		return d.raw(out, method, whole)
	}
	if it.Phase != "" {
		st.phase = it.Phase
	}
	if !st.open {
		if text == "" {
			return out // nothing to show yet (a delta opens the part), or ever
		}
		out = d.openText(out, it.ID, kind, st.phase)
	}
	switch {
	case strings.HasPrefix(text, st.sent):
		out = d.sendText(out, it.ID, text[len(st.sent):])
	case done:
		out = d.raw(out, method+".rewrite", whole) // final below is the item's text
	}
	if !done {
		return out
	}
	final := map[string]string{"text": text}
	for k, v := range extra {
		final[k] = v
	}
	return d.endText(out, it.ID, mustJSON(final))
}

// toolName is the stable name a tool_call part carries.
func toolName(it item) string {
	switch it.Type {
	case "commandExecution":
		return "shell"
	case "fileChange":
		return "apply_patch"
	case "webSearch":
		return "web_search"
	}
	return firstNonEmpty(it.Tool, it.Type)
}

// toolDetail is the call's short label: the command as the model wrote it, the
// first changed path, the query, the collaboration prompt.
func toolDetail(it item) string {
	switch it.Type {
	case "commandExecution":
		if len(it.CommandActions) == 1 && it.CommandActions[0].Command != "" {
			return it.CommandActions[0].Command
		}
		return it.Command
	case "fileChange":
		if ch := changes(it.Changes); len(ch) > 0 {
			return ch[0].Path
		}
	case "webSearch":
		return it.Query
	case "collabAgentToolCall":
		if it.Prompt != nil {
			return *it.Prompt
		}
	}
	return ""
}

type change struct {
	Path string `json:"path"`
	Kind struct {
		Type string `json:"type"`
	} `json:"kind"`
}

func changes(raw json.RawMessage) []change {
	var ch []change
	_ = json.Unmarshal(raw, &ch)
	return ch
}

func toolArgs(it item) json.RawMessage {
	switch it.Type {
	case "commandExecution":
		return mustJSON(map[string]string{"command": it.Command, "cwd": it.Cwd})
	case "mcpToolCall":
		return mustJSON(map[string]json.RawMessage{"server": mustJSON(it.Server), "tool": mustJSON(it.Tool), "arguments": orNull(it.Arguments)})
	case "fileChange":
		return mustJSON(map[string]json.RawMessage{"changes": orNull(it.Changes)})
	case "webSearch":
		return mustJSON(map[string]json.RawMessage{"query": mustJSON(it.Query), "action": orNull(it.Action)})
	case "dynamicToolCall":
		return mustJSON(map[string]json.RawMessage{"namespace": mustJSON(it.Namespace), "tool": mustJSON(it.Tool), "arguments": orNull(it.Arguments)})
	default: // collabAgentToolCall
		return mustJSON(map[string]json.RawMessage{"tool": mustJSON(it.Tool), "prompt": mustJSON(it.Prompt), "model": mustJSON(it.Model),
			"sender_thread_id": mustJSON(it.Sender), "receiver_thread_ids": orNull(it.Receivers)})
	}
}

func (d *decoder) tool(out []chatstream.Event, it item, st *itemState, done bool) []chatstream.Event {
	if !st.called {
		st.called = true
		out = d.message(out)
		meta := map[string]json.RawMessage{chatstream.MetaName: mustJSON(toolName(it)), "item_type": mustJSON(it.Type)}
		if detail := toolDetail(it); detail != "" {
			meta[chatstream.MetaDetail] = mustJSON(detail)
		}
		if it.Server != "" {
			meta["server"] = mustJSON(it.Server)
		}
		if it.Status != "" {
			meta["status"] = mustJSON(it.Status)
		}
		ps := d.Event(chatstream.VerbPartStart)
		ps.PartID, ps.Kind, ps.Meta = it.ID, string(chatstream.PartToolCall), meta
		out = d.Emit(out, ps)
		args := toolArgs(it)
		dl := d.Event(chatstream.VerbPartDelta)
		dl.PartID, dl.JSONFragment = it.ID, string(args)
		out = d.Emit(out, dl)
		en := d.Event(chatstream.VerbPartEnd)
		en.PartID, en.Final = it.ID, args
		out = d.Emit(out, en)
	}
	if !done || st.resulted {
		return out
	}
	st.resulted = true
	id := it.ID + "#result"
	isErr := isError(it)
	ps := d.Event(chatstream.VerbPartStart)
	ps.PartID, ps.Kind = id, string(chatstream.PartToolResult)
	ps.Meta = map[string]json.RawMessage{chatstream.MetaCallID: mustJSON(it.ID), chatstream.MetaIsError: mustJSON(isErr)}
	out = d.Emit(out, ps)
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID, en.Final = id, toolResult(it, isErr)
	return d.Emit(out, en)
}

func isError(it item) bool {
	switch it.Type {
	case "commandExecution":
		return it.Status == "failed" || it.Status == "declined" || (it.ExitCode != nil && *it.ExitCode != 0)
	case "mcpToolCall":
		return it.Status == "failed" || present(it.Error)
	case "fileChange":
		return it.Status == "failed" || it.Status == "declined"
	case "dynamicToolCall":
		return it.Status == "failed" || (it.Success != nil && !*it.Success)
	}
	return it.Status == "failed"
}

// toolResult is a tool_result part's final: the result as text, is_error, the
// item's status, and the item's structured outputs.
func toolResult(it item, isErr bool) json.RawMessage {
	r := map[string]json.RawMessage{"is_error": mustJSON(isErr)}
	if it.Status != "" {
		r["status"] = mustJSON(it.Status)
	}
	put := func(k string, v json.RawMessage) {
		if present(v) {
			r[k] = compact(v)
		}
	}
	text := ""
	switch it.Type {
	case "commandExecution":
		if it.AggregatedOutput != nil {
			text = *it.AggregatedOutput
		}
		if it.ExitCode != nil {
			r["exit_code"] = mustJSON(*it.ExitCode)
		}
	case "mcpToolCall":
		text = mcpText(it)
		put("result", it.Result)
		put("error", it.Error)
	case "fileChange":
		var lines []string
		for _, c := range changes(it.Changes) {
			lines = append(lines, strings.TrimSpace(c.Kind.Type+" "+c.Path))
		}
		text = strings.Join(lines, "\n")
		put("changes", it.Changes)
	case "webSearch":
		if present(it.Results) {
			text = string(compact(it.Results))
		}
		put("results", it.Results)
	case "dynamicToolCall":
		text = inputText(it.ContentItems)
		put("content_items", it.ContentItems)
	default: // collabAgentToolCall
		if present(it.AgentsStates) {
			text = string(compact(it.AgentsStates))
		}
		put("agents_states", it.AgentsStates)
	}
	if it.DurationMS != nil {
		r["duration_ms"] = mustJSON(*it.DurationMS)
	}
	r["text"] = mustJSON(text)
	return mustJSON(r)
}

// mcpText is an MCP call's result as text: the error message, else the text
// content blocks, else the content as JSON.
func mcpText(it item) string {
	var e struct {
		Message string `json:"message"`
	}
	if present(it.Error) && json.Unmarshal(it.Error, &e) == nil && e.Message != "" {
		return e.Message
	}
	var res struct {
		Content []json.RawMessage `json:"content"`
	}
	if !present(it.Result) || json.Unmarshal(it.Result, &res) != nil {
		return ""
	}
	var texts []string
	for _, c := range res.Content {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(c, &b) == nil && b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n")
	}
	if len(res.Content) > 0 {
		return string(mustJSON(res.Content))
	}
	return ""
}

// stringsOf reads a JSON array of strings; anything else is none.
func stringsOf(raw json.RawMessage) []string {
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func inputText(raw json.RawMessage) string {
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &items)
	var texts []string
	for _, c := range items {
		if c.Type == "inputText" {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// tokenUsage records the thread's running totals; the turn's usage rides on
// run.finish.
func (d *decoder) tokenUsage(params json.RawMessage, c common, whole []byte) []chatstream.Event {
	out := d.start(c.ThreadID, "")
	var p struct {
		TokenUsage tokenUsage `json:"tokenUsage"`
	}
	if json.Unmarshal(params, &p) != nil || p.TokenUsage.Total == nil || p.TokenUsage.Last == nil ||
		(d.turnID != "" && c.TurnID != "" && c.TurnID != d.turnID) {
		return d.raw(out, "thread/tokenUsage/updated", whole)
	}
	total, last := *p.TokenUsage.Total, *p.TokenUsage.Last
	if d.usage == nil {
		d.usage = &usageTrack{before: breakdown{
			Input: total.Input - last.Input, Cached: total.Cached - last.Cached, CacheWrite: total.CacheWrite - last.CacheWrite,
			Output: total.Output - last.Output, Reasoning: total.Reasoning - last.Reasoning,
		}}
	}
	d.usage.latest = total
	return out
}

// turnUsage is the turn's usage as disjoint components.
func (d *decoder) turnUsage() (chatstream.Usage, error) {
	conv := func(b breakdown) (chatstream.Usage, error) {
		return chatstream.UsageFromInclusive(chatstream.UsageFinal, b.Input, b.Cached, b.CacheWrite, b.Output, b.Reasoning)
	}
	latest, err := conv(d.usage.latest)
	if err != nil {
		return chatstream.Usage{}, err
	}
	before, err := conv(d.usage.before)
	if err != nil {
		return chatstream.Usage{}, err
	}
	u, err := latest.Sub(before)
	if err != nil {
		return chatstream.Usage{}, err
	}
	u.Scope = chatstream.UsageFinal
	return u, nil
}

// turnCompleted ends the run by the turn's status: completed is run.finish,
// interrupted run.abort, failed (or anything else) run.error.
func (d *decoder) turnCompleted(params json.RawMessage) []chatstream.Event {
	var p struct {
		ThreadID string `json:"threadId"`
		Turn     turn   `json:"turn"`
	}
	_ = json.Unmarshal(params, &p)
	out := d.start(p.ThreadID, p.Turn.ID)
	out = d.endOpen(out)
	out = d.Unwind(out) // parts, then the message, then the step
	switch p.Turn.Status {
	case "completed":
		ev := d.Event(chatstream.VerbRunFinish)
		ev.Reason = string(chatstream.FinishStop)
		if d.usage != nil {
			if u, err := d.turnUsage(); err != nil {
				ev.Ext = map[string]json.RawMessage{extKey: mustJSON(map[string]string{"usage_error": err.Error()})}
			} else {
				ev.Usage = &u
			}
		}
		return d.Emit(out, ev)
	case "interrupted":
		ev := d.Event(chatstream.VerbRunAbort)
		ev.Reason = "interrupted"
		return d.Emit(out, ev)
	}
	ev := d.Event(chatstream.VerbRunError)
	var te turnError
	if present(p.Turn.Error) && json.Unmarshal(p.Turn.Error, &te) != nil {
		te.Message = p.Turn.Error
	}
	ev.Code, ev.Message = "turn_failed", errorMessage(te.Message)
	if p.Turn.Status != "failed" {
		ev.Code = chatstream.CodeUpstreamError
		if ev.Message == "" {
			ev.Message = fmt.Sprintf("turn/completed with status %q", p.Turn.Status)
		}
	}
	ext := map[string]json.RawMessage{}
	if present(te.CodexErrorInfo) {
		ext["codex_error_info"] = compact(te.CodexErrorInfo)
	}
	if present(te.AdditionalDetails) {
		ext["additional_details"] = compact(te.AdditionalDetails)
	}
	if len(ext) > 0 {
		ev.Ext = map[string]json.RawMessage{extKey: mustJSON(ext)}
	}
	return d.Emit(out, ev)
}

// readEnvelope reads a JSON-RPC message. An object whose members do not fit
// is not one, unless it is a turn/completed: that is still the end of the turn.
func readEnvelope(data []byte) (envelope, bool) {
	var env envelope
	if json.Unmarshal(data, &env) == nil {
		return env, true
	}
	var obj map[string]json.RawMessage
	var m string
	if json.Unmarshal(data, &obj) == nil && json.Unmarshal(obj["method"], &m) == nil && m == "turn/completed" {
		return envelope{Method: m, Params: obj["params"]}, true
	}
	return envelope{}, false
}

// errorMessage is the text of an error message. Codex often relays the
// upstream's JSON error body as the message; its inner error.message (or
// message) is used when there is one. A message that is not a string is its
// JSON text.
func errorMessage(raw json.RawMessage) string {
	if !present(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return string(compact(raw))
	}
	var body struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") && json.Unmarshal([]byte(t), &body) == nil {
		if m := firstNonEmpty(body.Error.Message, body.Message); m != "" {
			return m
		}
	}
	return s
}

// pick returns an object of the named members of params that are present.
func pick(params json.RawMessage, keys ...string) json.RawMessage {
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(params, &obj)
	out := map[string]json.RawMessage{}
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			out[k] = compact(v)
		}
	}
	return mustJSON(out)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func present(r json.RawMessage) bool {
	s := bytes.TrimSpace(r)
	return len(s) > 0 && string(s) != "null"
}

func orNull(r json.RawMessage) json.RawMessage {
	if !present(r) {
		return json.RawMessage(`null`)
	}
	return compact(r)
}

func compact(b []byte) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return mustJSON(string(b))
	}
	return buf.Bytes()
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}
