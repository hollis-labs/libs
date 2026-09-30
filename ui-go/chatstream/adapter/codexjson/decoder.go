package codexjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/internal/decodekit"
)

type line struct {
	Type     string                    `json:"type"`
	ThreadID string                    `json:"thread_id"`
	Message  string                    `json:"message"`
	Usage    *rawUsage                 `json:"usage"`
	Error    *struct{ Message string } `json:"error"`
	Item     json.RawMessage           `json:"item"`
}

type rawUsage struct {
	Input      int `json:"input_tokens"`
	Cached     int `json:"cached_input_tokens"`
	CacheWrite int `json:"cache_write_input_tokens"`
	Output     int `json:"output_tokens"`
	Reasoning  int `json:"reasoning_output_tokens"`
}

type item struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Command          string          `json:"command"`
	AggregatedOutput json.RawMessage `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
	Status           string          `json:"status"`
	Changes          json.RawMessage `json:"changes"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           json.RawMessage `json:"result"`
	Error            json.RawMessage `json:"error"`
	Query            string          `json:"query"`
	Action           json.RawMessage `json:"action"`
	Results          json.RawMessage `json:"results"`
	Prompt           string          `json:"prompt"`
	Sender           string          `json:"sender_thread_id"`
	Receivers        json.RawMessage `json:"receiver_thread_ids"`
	AgentsStates     json.RawMessage `json:"agents_states"`
	Items            json.RawMessage `json:"items"`
	Message          string          `json:"message"`
}

type itemState struct {
	partID   string // the text/reasoning part, "" if none is open
	sent     string
	called   bool
	resulted bool
}

type decoder struct {
	*decodekit.Base
	cfg     config
	items   map[string]*itemState
	turns   int
	msgOpen bool
	msgSeq  int
}

func newDecoder(o chatstream.DecodeOptions, cfg config) *decoder {
	return &decoder{Base: decodekit.New(o), cfg: cfg, items: map[string]*itemState{}}
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
	var l line
	if !parses(f.Data, &l) || l.Type == "" {
		return d.raw(d.start(""), "malformed", f.Data), nil
	}
	switch l.Type {
	case "thread.started":
		if d.Started() {
			return d.raw(nil, l.Type, f.Data), nil
		}
		return d.start(l.ThreadID), nil
	case "turn.started":
		out := d.start("")
		d.turns++
		ev := d.Event(chatstream.VerbStepStart)
		ev.StepID = fmt.Sprintf("turn-%d", d.turns)
		return d.Emit(out, ev), nil
	case "item.started", "item.updated", "item.completed":
		return d.itemEvent(l, f.Data), nil
	case "turn.completed":
		return d.completed(l), nil
	case "turn.failed":
		msg := ""
		if l.Error != nil {
			msg = l.Error.Message
		}
		return d.fail("turn_failed", msg), nil
	case "error":
		return d.fail(chatstream.CodeUpstreamError, l.Message), nil
	}
	return d.raw(d.start(""), l.Type, f.Data), nil
}

// start emits run.start once; threadID, when the line carries one, goes in Ext
// and becomes the run id if none was configured.
func (d *decoder) start(threadID string) []chatstream.Event {
	if d.Started() {
		return nil
	}
	if threadID != "" {
		d.SetRunID(threadID)
	}
	ev := d.Event(chatstream.VerbRunStart)
	ev.Provider, ev.Model = d.Options().Provider, d.Options().Model
	if threadID != "" {
		ev.Ext = map[string]json.RawMessage{extKey: mustJSON(map[string]string{"thread_id": threadID})}
	}
	return d.Emit(nil, ev)
}

func (d *decoder) raw(out []chatstream.Event, typ string, payload []byte) []chatstream.Event {
	return d.Emit(out, d.RawEvent(extKey, typ, payload))
}

func (d *decoder) message(out []chatstream.Event) []chatstream.Event {
	if d.msgOpen {
		return out
	}
	d.msgOpen = true
	d.msgSeq++
	ev := d.Event(chatstream.VerbMessageStart)
	ev.MessageID, ev.Role = fmt.Sprintf("msg-%d", d.msgSeq), "assistant"
	return d.Emit(out, ev)
}

func (d *decoder) itemEvent(l line, whole []byte) []chatstream.Event {
	out := d.start("")
	var it item
	if err := json.Unmarshal(l.Item, &it); err != nil || it.ID == "" {
		return d.raw(out, l.Type, whole)
	}
	st := d.items[it.ID]
	if st == nil {
		st = &itemState{}
		d.items[it.ID] = st
	}
	done := l.Type == "item.completed"
	switch it.Type {
	case "agent_message":
		return d.text(out, chatstream.PartText, it, st, done, whole, l.Type)
	case "reasoning":
		return d.text(out, chatstream.PartReasoning, it, st, done, whole, l.Type)
	case "todo_list":
		v := map[string]json.RawMessage{"items": orNull(it.Items)}
		if it.Status != "" {
			v["status"] = mustJSON(it.Status)
		}
		return d.activity(out, "codex.todo_list", mustJSON(v), nil)
	case "error":
		return d.activity(out, "codex.error", mustJSON(map[string]string{"message": it.Message}), d.RawEvent(extKey, l.Type, whole).Raw)
	case "command_execution", "mcp_tool_call", "file_change", "web_search", "collab_tool_call":
		if l.Type == "item.updated" {
			return d.raw(out, l.Type, whole)
		}
		return d.tool(out, it, st, done)
	}
	return d.raw(out, l.Type+"."+it.Type, whole)
}

func (d *decoder) activity(out []chatstream.Event, kind string, value json.RawMessage, raw *chatstream.Raw) []chatstream.Event {
	ev := d.Event(chatstream.VerbActivity)
	ev.Kind, ev.Value, ev.Raw = kind, value, raw
	return d.Emit(out, ev)
}

// text streams a whole-text item. Codex sends the text at item.completed; text
// that appears earlier is streamed as it grows.
func (d *decoder) text(out []chatstream.Event, kind chatstream.PartKind, it item, st *itemState, done bool, whole []byte, typ string) []chatstream.Event {
	if st.partID == "" && st.sent == "" && it.Text == "" {
		return out // nothing to show yet, or ever
	}
	if st.partID == "" {
		if st.resulted { // already finished: a repeat
			return d.raw(out, typ, whole)
		}
		out = d.message(out)
		ps := d.Event(chatstream.VerbPartStart)
		ps.PartID, ps.Kind = it.ID, string(kind)
		out = d.Emit(out, ps)
		st.partID = it.ID
	}
	switch {
	case strings.HasPrefix(it.Text, st.sent):
		if grow := it.Text[len(st.sent):]; grow != "" {
			dl := d.Event(chatstream.VerbPartDelta)
			dl.PartID, dl.Text = it.ID, grow
			out = d.Emit(out, dl)
			st.sent = it.Text
		}
	default:
		out = d.raw(out, typ+".rewrite", whole)
	}
	if done {
		en := d.Event(chatstream.VerbPartEnd)
		en.PartID = it.ID
		out = d.Emit(out, en)
		st.partID, st.resulted = "", true
	}
	return out
}

// toolName is the stable name a tool_call part carries: shell commands are
// "shell", patches "apply_patch", searches "web_search", and MCP and
// collaboration calls their own tool name.
func (d *decoder) toolName(it item) string {
	switch it.Type {
	case "command_execution":
		return "shell"
	case "file_change":
		return "apply_patch"
	case "web_search":
		return "web_search"
	}
	return firstNonEmpty(it.Tool, it.Type)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// toolDetail is the short label for the call: the command, the first changed
// path, the query, the collaboration prompt.
func (d *decoder) toolDetail(it item) string {
	switch it.Type {
	case "command_execution":
		return it.Command
	case "file_change":
		var ch []struct {
			Path string `json:"path"`
		}
		if parses(it.Changes, &ch) && len(ch) > 0 {
			return ch[0].Path
		}
	case "web_search":
		return it.Query
	case "collab_tool_call":
		return it.Prompt
	}
	return ""
}

func (d *decoder) toolArgs(it item) json.RawMessage {
	switch it.Type {
	case "command_execution":
		return mustJSON(map[string]string{"command": it.Command})
	case "mcp_tool_call":
		return mustJSON(map[string]json.RawMessage{"server": mustJSON(it.Server), "tool": mustJSON(it.Tool), "arguments": orNull(it.Arguments)})
	case "file_change":
		return mustJSON(map[string]json.RawMessage{"changes": orNull(it.Changes)})
	case "web_search":
		return mustJSON(map[string]json.RawMessage{"query": mustJSON(it.Query), "action": orNull(it.Action)})
	default: // collab_tool_call
		return mustJSON(map[string]json.RawMessage{"tool": mustJSON(it.Tool), "prompt": mustJSON(it.Prompt),
			"sender_thread_id": mustJSON(it.Sender), "receiver_thread_ids": orNull(it.Receivers)})
	}
}

func (d *decoder) tool(out []chatstream.Event, it item, st *itemState, done bool) []chatstream.Event {
	if !st.called {
		st.called = true
		out = d.message(out)
		meta := map[string]json.RawMessage{chatstream.MetaName: mustJSON(d.toolName(it)), "item_type": mustJSON(it.Type)}
		if detail := d.toolDetail(it); detail != "" {
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
		args := d.toolArgs(it)
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
	ps := d.Event(chatstream.VerbPartStart)
	ps.PartID, ps.Kind = id, string(chatstream.PartToolResult)
	ps.Meta = map[string]json.RawMessage{chatstream.MetaCallID: mustJSON(it.ID), chatstream.MetaIsError: mustJSON(isError(it))}
	out = d.Emit(out, ps)
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID, en.Final = id, d.toolResult(it)
	return d.Emit(out, en)
}

func isError(it item) bool {
	switch it.Type {
	case "command_execution":
		return it.Status == "failed" || it.Status == "declined" || (it.ExitCode != nil && *it.ExitCode != 0)
	case "mcp_tool_call":
		return it.Status == "failed" || present(it.Error)
	}
	return it.Status == "failed"
}

func (d *decoder) toolResult(it item) json.RawMessage {
	r := map[string]json.RawMessage{"status": mustJSON(it.Status)}
	put := func(k string, v json.RawMessage) {
		if present(v) {
			r[k] = compact(v)
		}
	}
	switch it.Type {
	case "command_execution":
		put("aggregated_output", it.AggregatedOutput)
		if it.ExitCode != nil {
			r["exit_code"] = mustJSON(*it.ExitCode)
		}
	case "mcp_tool_call":
		put("result", it.Result)
		put("error", it.Error)
	case "file_change":
		put("changes", it.Changes)
	case "web_search":
		put("results", it.Results)
	default:
		put("agents_states", it.AgentsStates)
	}
	return mustJSON(r)
}

func (d *decoder) completed(l line) []chatstream.Event {
	out := d.start("")
	out = d.Unwind(out) // parts, then the message, then the step
	ev := d.Event(chatstream.VerbRunFinish)
	ev.Reason = string(chatstream.FinishStop)
	if l.Usage != nil {
		u, err := d.usage(*l.Usage)
		if err != nil {
			ev.Ext = map[string]json.RawMessage{extKey: mustJSON(map[string]string{"usage_error": err.Error()})}
		} else {
			ev.Usage = &u
		}
	}
	return d.Emit(out, ev)
}

// usage converts Codex's inclusive counters to disjoint components and, when the
// stream is declared cumulative, to the change since the baseline.
func (d *decoder) usage(r rawUsage) (chatstream.Usage, error) {
	u, err := chatstream.UsageFromInclusive(chatstream.UsageFinal, r.Input, r.Cached, r.CacheWrite, r.Output, r.Reasoning)
	if err != nil {
		return chatstream.Usage{}, err
	}
	if d.cfg.baseline != nil {
		delta, err := u.Sub(*d.cfg.baseline)
		if err != nil {
			return chatstream.Usage{}, err
		}
		delta.Scope = chatstream.UsageFinal
		return delta, nil
	}
	return u, nil
}

func (d *decoder) fail(code, msg string) []chatstream.Event {
	out := d.start("")
	out = d.Unwind(out)
	ev := d.Event(chatstream.VerbRunError)
	ev.Code, ev.Message = code, msg
	return d.Emit(out, ev)
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

// parses reports whether data is JSON that fits v.
func parses(data []byte, v any) bool { return json.Unmarshal(data, v) == nil }
