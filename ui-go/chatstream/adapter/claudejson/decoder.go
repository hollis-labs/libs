package claudejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/internal/anthropicwire"
	"github.com/hollis-labs/go-chatstream/internal/decodekit"
)

type decoder struct {
	*decodekit.Base
	m        *anthropicwire.Machine
	whole    string         // id of the whole (non-streamed) assistant message that is open
	counters map[string]int // next part index per whole message id
	users    int
}

func newDecoder(o chatstream.DecodeOptions) *decoder {
	b := decodekit.New(o)
	return &decoder{
		Base:     b,
		m:        anthropicwire.NewMachine(b, anthropicwire.Config{Dialect: DialectName}),
		counters: map[string]int{},
	}
}

// Close ends the stream: nothing if a result ended the run, else the open parts
// are closed and run.error with code upstream_truncated ends it.
func (d *decoder) Close(cause error) []chatstream.Event { return d.Base.Close(cause) }

func (d *decoder) raw(typ string, data []byte) chatstream.Event {
	ev := d.Event(chatstream.VerbRaw)
	payload := bytes.TrimSpace(data)
	if !json.Valid(payload) {
		payload, _ = json.Marshal(string(payload))
	}
	ev.Raw = &chatstream.Raw{Dialect: DialectName, Type: typ, Payload: append(json.RawMessage(nil), payload...)}
	return ev
}

func (d *decoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	if d.Terminated() || len(bytes.TrimSpace(f.Data)) == 0 {
		return nil, nil
	}
	var head struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(f.Data, &head); err != nil {
		return d.Emit(nil, d.raw("malformed", f.Data)), nil //nolint:nilerr // a malformed line is a raw event, not a decoder failure
	}
	var out []chatstream.Event
	if !d.Started() {
		d.SetRunID(head.SessionID)
	}
	if head.Type == "system" && head.Subtype == "init" && !d.Started() {
		return d.init(out, f.Data), nil
	}
	out = d.startRun(out, f.Data, head.Type)
	switch head.Type {
	case "system":
		ev := d.Event(chatstream.VerbActivity)
		ev.Kind, ev.Value = "claude.system."+head.Subtype, compact(f.Data)
		return d.Emit(out, ev), nil
	case "stream_event":
		return d.streamEvent(out, f.Data), nil
	case "assistant":
		return d.assistant(out, f.Data), nil
	case "user":
		return d.user(out, f.Data), nil
	case "control_request":
		return d.controlRequest(out, f.Data), nil
	case "result":
		return d.result(out, f.Data), nil
	}
	return d.Emit(out, d.raw(head.Type, f.Data)), nil
}

// startRun opens the run if a line other than system/init arrives first.
func (d *decoder) startRun(out []chatstream.Event, _ []byte, _ string) []chatstream.Event {
	if d.Started() {
		return out
	}
	ev := d.Event(chatstream.VerbRunStart)
	ev.Provider, ev.Model = d.provider(), d.Options().Model
	return d.Emit(out, ev)
}

func (d *decoder) provider() string {
	if p := d.Options().Provider; p != "" {
		return p
	}
	return "claude"
}

func (d *decoder) init(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(data, &w)
	ev := d.Event(chatstream.VerbRunStart)
	ev.Provider, ev.Model = d.provider(), w.Model
	if ev.Model == "" {
		ev.Model = d.Options().Model
	}
	ev.Ext = map[string]json.RawMessage{"claude": compact(data)}
	return d.Emit(out, ev)
}

func (d *decoder) streamEvent(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(data, &w); err != nil || len(w.Event) == 0 {
		return d.Emit(out, d.raw("stream_event", data))
	}
	var head struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(w.Event, &head)
	if head.Type == "message_start" {
		out = d.closeWhole(out) // a new streamed message ends any whole one
	}
	out, handled := d.m.Handle(out, "", w.Event)
	if !handled {
		out = d.Emit(out, d.raw("stream_event."+head.Type, w.Event))
	}
	return out
}

// closeWhole ends the open whole assistant message, if any.
func (d *decoder) closeWhole(out []chatstream.Event) []chatstream.Event {
	if d.whole == "" {
		return out
	}
	me := d.Event(chatstream.VerbMessageEnd)
	me.MessageID = d.whole
	out = d.Emit(out, me)
	sf := d.Event(chatstream.VerbStepFinish)
	sf.StepID = d.whole
	out = d.Emit(out, sf)
	d.whole = ""
	return out
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   *bool           `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func (d *decoder) assistant(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Message struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Parent  *string         `json:"parent_tool_use_id"`
		Aborted bool            `json:"aborted"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return d.Emit(out, d.raw("assistant", data))
	}
	id := w.Message.ID
	if !d.m.Streamed(id) {
		var all, blocks []json.RawMessage
		_ = json.Unmarshal(w.Message.Content, &all)
		for _, raw := range all {
			if !emptyText(raw) { // the CLI emits blocks as they complete; an empty text block says nothing
				blocks = append(blocks, raw)
			}
		}
		if len(blocks) > 0 {
			out = d.openWhole(out, id)
			for _, raw := range blocks {
				out = d.wholeBlock(out, id, raw, w.Parent)
			}
		}
	}
	if w.Aborted {
		ev := d.Event(chatstream.VerbActivity)
		ev.Kind, ev.Value = "claude.assistant_aborted", mustJSON(map[string]string{"message_id": id})
		out = d.Emit(out, ev)
	}
	if isPresent(w.Error) {
		ev := d.Event(chatstream.VerbActivity)
		ev.Kind, ev.Value = "claude.assistant_error", mustJSON(map[string]json.RawMessage{"message_id": mustJSON(id), "error": w.Error})
		out = d.Emit(out, ev)
	}
	return out
}

func (d *decoder) openWhole(out []chatstream.Event, id string) []chatstream.Event {
	if d.whole == id {
		return out
	}
	out = d.closeWhole(out)
	st := d.Event(chatstream.VerbStepStart)
	st.StepID = id
	out = d.Emit(out, st)
	ms := d.Event(chatstream.VerbMessageStart)
	ms.MessageID, ms.Role = id, "assistant"
	out = d.Emit(out, ms)
	d.whole = id
	return out
}

func (d *decoder) partID(msg string) string {
	n := d.counters[msg]
	d.counters[msg] = n + 1
	return fmt.Sprintf("%s:%d", msg, n)
}

// wholeBlock emits one complete content block as start, one delta, end.
func (d *decoder) wholeBlock(out []chatstream.Event, msg string, raw json.RawMessage, parent *string) []chatstream.Event {
	var cb contentBlock
	if err := json.Unmarshal(raw, &cb); err != nil {
		return d.Emit(out, d.raw("assistant.content", raw))
	}
	meta := map[string]json.RawMessage{}
	if parent != nil {
		meta["parent_tool_use_id"] = mustJSON(*parent)
	}
	start := d.Event(chatstream.VerbPartStart)
	start.PartID = d.partID(msg)
	end := d.Event(chatstream.VerbPartEnd)
	end.PartID = start.PartID
	var deltaText, deltaJSON string
	switch {
	case cb.Type == "text":
		start.Kind, deltaText = string(chatstream.PartText), cb.Text
	case cb.Type == "thinking":
		start.Kind, deltaText = string(chatstream.PartReasoning), cb.Thinking
		if cb.Signature != "" {
			end.Final = mustJSON(map[string]string{"signature": cb.Signature})
		}
	case cb.Type == "redacted_thinking":
		start.Kind = string(chatstream.PartReasoning)
		meta["redacted"] = json.RawMessage("true")
		end.Final = mustJSON(map[string]string{"data": cb.Data})
	case cb.Type == "tool_use" || cb.Type == "server_tool_use" || cb.Type == "mcp_tool_use":
		start.Kind = string(chatstream.PartToolCall)
		meta[chatstream.MetaName], meta["id"] = mustJSON(cb.Name), mustJSON(cb.ID)
		d.m.RegisterCall(cb.ID, start.PartID)
		if cb.Type != "tool_use" {
			meta["server"] = json.RawMessage("true")
			meta["block_type"] = mustJSON(cb.Type)
		}
		args := compact(cb.Input)
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		end.Final = args
		if string(args) != "{}" {
			deltaJSON = string(args)
		}
	case strings.HasSuffix(cb.Type, "tool_result"):
		meta["block_type"] = mustJSON(cb.Type)
		meta["tool_use_id"] = mustJSON(cb.ToolUseID)
		end.Final = compact(raw)
		if call, known := d.m.CallPart(cb.ToolUseID); known {
			start.Kind = string(chatstream.PartToolResult)
			meta[chatstream.MetaCallID] = mustJSON(call)
			if cb.Type != "tool_result" {
				meta["server"] = json.RawMessage("true")
			}
			if anthropicwire.ResultIsError(cb.IsError, cb.Content) {
				meta[chatstream.MetaIsError] = json.RawMessage("true")
			}
		} else {
			start.Kind = string(chatstream.PartData) // answers a call this stream never showed
		}
	default:
		start.Kind = string(chatstream.PartData)
		meta["block_type"] = mustJSON(cb.Type)
		end.Final = compact(raw)
	}
	if len(meta) > 0 {
		start.Meta = meta
	}
	out = d.Emit(out, start)
	if deltaText != "" || deltaJSON != "" {
		dl := d.Event(chatstream.VerbPartDelta)
		dl.PartID, dl.Text, dl.JSONFragment = start.PartID, deltaText, deltaJSON
		out = d.Emit(out, dl)
	}
	return d.Emit(out, end)
}

func (d *decoder) user(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		UUID    string  `json:"uuid"`
		Parent  *string `json:"parent_tool_use_id"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return d.Emit(out, d.raw("user", data))
	}
	var blocks []json.RawMessage
	if json.Unmarshal(w.Message.Content, &blocks) != nil {
		return d.Emit(out, d.raw("user", data)) // a plain string prompt
	}
	var results []json.RawMessage
	for _, b := range blocks {
		var cb contentBlock
		if json.Unmarshal(b, &cb) == nil && cb.Type == "tool_result" {
			results = append(results, b)
		}
	}
	if len(results) == 0 {
		return d.Emit(out, d.raw("user", data))
	}
	out = d.closeWhole(out)
	d.users++
	id := w.UUID
	if id == "" {
		id = fmt.Sprintf("user-%d", d.users)
	}
	ms := d.Event(chatstream.VerbMessageStart)
	ms.MessageID, ms.Role = id, "user"
	out = d.Emit(out, ms)
	for _, r := range results {
		out = d.wholeBlock(out, id, r, w.Parent)
	}
	me := d.Event(chatstream.VerbMessageEnd)
	me.MessageID = id
	return d.Emit(out, me)
}

func (d *decoder) controlRequest(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return d.Emit(out, d.raw("control_request", data))
	}
	var r struct {
		Subtype     string `json:"subtype"`
		ToolName    string `json:"tool_name"`
		ToolUseID   string `json:"tool_use_id"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(w.Request, &r)
	if w.RequestID == "" || (r.Subtype != "can_use_tool" && r.ToolName == "") {
		return d.Emit(out, d.raw("control_request", data))
	}
	ev := d.Event(chatstream.VerbApprovalRequest)
	ev.ApprovalID, ev.Mode = w.RequestID, chatstream.ApprovalInBand
	if call, known := d.m.CallPart(r.ToolUseID); known {
		ev.CallID = call // the tool_call part it gates; the raw tool_use_id stays in the descriptor
	}
	ev.Reason = r.Description
	if ev.Reason == "" {
		ev.Reason = "can_use_tool"
	}
	ev.Descriptor = compact(w.Request)
	return d.Emit(out, ev)
}

func (d *decoder) result(out []chatstream.Event, data []byte) []chatstream.Event {
	// The result frame is the terminal one, so every field but the envelope is
	// read leniently: a field of an unexpected type (duration_ms as a float, errors
	// as objects, result as an object) must not demote the frame to a raw event and
	// turn a finished or failed run into a retryable truncation.
	var w struct {
		Subtype           string          `json:"subtype"`
		IsError           json.RawMessage `json:"is_error"`
		Result            json.RawMessage `json:"result"`
		StopReason        json.RawMessage `json:"stop_reason"`
		TerminalReason    json.RawMessage `json:"terminal_reason"`
		Errors            json.RawMessage `json:"errors"`
		NumTurns          json.RawMessage `json:"num_turns"`
		DurationMS        json.RawMessage `json:"duration_ms"`
		TotalCostUSD      json.RawMessage `json:"total_cost_usd"`
		PermissionDenials json.RawMessage `json:"permission_denials"`
		Usage             json.RawMessage `json:"usage"`
	}
	// A type error in one field does not stop json.Unmarshal filling the rest, so
	// only a frame that is not a JSON object at all is demoted to raw.
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return d.Emit(out, d.raw("result", data))
	}
	_ = json.Unmarshal(data, &w)
	out = d.m.CloseBlocks(out)
	out = d.closeWhole(out)
	out = d.Unwind(out)

	var usage *chatstream.Usage
	var wu struct {
		Input  float64 `json:"input_tokens"`
		Output float64 `json:"output_tokens"`
		CacheW float64 `json:"cache_creation_input_tokens"`
		CacheR float64 `json:"cache_read_input_tokens"`
	}
	if isPresent(w.Usage) && json.Unmarshal(w.Usage, &wu) == nil {
		if u, err := chatstream.UsageFromExclusiveInput(chatstream.UsageFinal, int(wu.Input), int(wu.CacheR), int(wu.CacheW), int(wu.Output)); err == nil {
			usage = &u
		}
	}
	isError := rawBool(w.IsError)
	stop := rawString(w.StopReason)
	terminalReason := rawString(w.TerminalReason)
	errTexts := errorTexts(w.Errors)
	reason := terminalReason
	if reason == "" && w.Subtype == "error_max_turns" {
		reason = "max_turns"
	}
	rawReason := reason
	if (reason == "" || reason == "completed") && stop != "" {
		rawReason = stop // the API stop_reason is what the finish reason is mapped from
	}
	ext := map[string]json.RawMessage{}
	put := func(k string, v any) { ext[k] = mustJSON(v) }
	put("subtype", w.Subtype)
	if terminalReason != "" {
		put("terminal_reason", terminalReason)
	}
	if stop != "" {
		put("stop_reason", stop)
	}
	for k, raw := range map[string]json.RawMessage{"num_turns": w.NumTurns, "duration_ms": w.DurationMS, "total_cost_usd": w.TotalCostUSD} {
		if isPresent(raw) {
			if c := compact(raw); c != nil {
				ext[k] = c
			}
		}
	}
	if isPresent(w.PermissionDenials) && string(compact(w.PermissionDenials)) != "[]" {
		ext["permission_denials"] = compact(w.PermissionDenials)
	}
	if len(errTexts) > 0 {
		put("errors", errTexts)
	}
	extra := map[string]json.RawMessage{"claude": mustJSON(ext)}

	standaloneUsage := func(out []chatstream.Event) []chatstream.Event {
		if usage == nil {
			return out
		}
		ev := d.Event(chatstream.VerbUsage)
		ev.Usage = usage
		return d.Emit(out, ev)
	}
	finish := func(out []chatstream.Event, fr chatstream.FinishReason) []chatstream.Event {
		ev := d.Event(chatstream.VerbRunFinish)
		ev.Reason, ev.RawReason, ev.Usage, ev.Ext = string(fr), rawReason, usage, extra
		return d.Emit(out, ev)
	}
	switch {
	case reason == "aborted_streaming" || reason == "aborted_tools":
		out = standaloneUsage(out)
		ev := d.Event(chatstream.VerbRunAbort)
		ev.Reason, ev.Ext = reason, extra
		return d.Emit(out, ev)
	case reason == "max_turns":
		return finish(out, chatstream.FinishTurnLimit)
	case reason == "prompt_too_long":
		return finish(out, chatstream.FinishContextExceeded)
	case isError || (w.Subtype != "" && w.Subtype != "success"):
		out = standaloneUsage(out)
		ev := d.Event(chatstream.VerbRunError)
		ev.Code = w.Subtype
		if ev.Code == "" || ev.Code == "success" {
			ev.Code = reason
		}
		if ev.Code == "" {
			ev.Code = chatstream.CodeUpstreamError
		}
		ev.Message = strings.Join(errTexts, "; ")
		if ev.Message == "" {
			ev.Message = rawString(w.Result)
		}
		ev.Retryable = reason == "api_error" || reason == "model_error"
		ev.Ext = extra
		return d.Emit(out, ev)
	case reason == "" || reason == "completed":
		fr := anthropicwire.FinishReason(stop)
		if stop == "" {
			fr = chatstream.FinishStop
		}
		return finish(out, fr)
	}
	return finish(out, chatstream.FinishOther)
}

func emptyText(raw json.RawMessage) bool {
	var cb contentBlock
	return json.Unmarshal(raw, &cb) == nil && cb.Type == "text" && cb.Text == ""
}

func compact(raw []byte) json.RawMessage {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return nil
	}
	return buf.Bytes()
}

func isPresent(r json.RawMessage) bool {
	s := strings.TrimSpace(string(r))
	return s != "" && s != "null"
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// rawString is a JSON string's value, "" for anything else.
func rawString(r json.RawMessage) string {
	var s string
	if json.Unmarshal(r, &s) != nil {
		return ""
	}
	return s
}

func rawBool(r json.RawMessage) bool {
	var b bool
	return json.Unmarshal(r, &b) == nil && b
}

// errorTexts reads the result frame's errors whatever their shape: an array of
// strings, an array of objects (message, error or text field, else the object's
// JSON), a single string or a single object.
func errorTexts(r json.RawMessage) []string {
	if !isPresent(r) {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(r, &items) != nil {
		items = []json.RawMessage{r}
	}
	var out []string
	for _, it := range items {
		if !isPresent(it) {
			continue
		}
		if s := rawString(it); s != "" {
			out = append(out, s)
			continue
		}
		var o struct {
			Message string `json:"message"`
			Error   string `json:"error"`
			Text    string `json:"text"`
		}
		if json.Unmarshal(it, &o) == nil {
			if s := firstNonEmpty(o.Message, o.Error, o.Text); s != "" {
				out = append(out, s)
				continue
			}
		}
		if c := compact(it); c != nil {
			out = append(out, string(c))
		}
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
