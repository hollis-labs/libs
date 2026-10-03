package openairesponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/internal/decodekit"
)

// unknownTool names a tool call whose item was never seen.
const unknownTool = "unknown"

// incompleteReasons maps incomplete_details.reason to the closed vocabulary.
var incompleteReasons = map[string]chatstream.FinishReason{
	"max_output_tokens": chatstream.FinishLength,
	"max_tokens":        chatstream.FinishLength, // in the reference example, not in the schema enum
	"max_messages":      chatstream.FinishTurnLimit,
	"content_filter":    chatstream.FinishContentFilter,
	"steered":           chatstream.FinishOther,
}

// retryableCodes are the ResponseErrorCode values worth retrying; every other
// documented code (invalid_prompt, the image_* family, policy violations,
// data_residency_mismatch, ...) is a request problem retrying cannot fix.
var retryableCodes = map[string]bool{
	"server_error":         true,
	"rate_limit_exceeded":  true,
	"vector_store_timeout": true,
}

// activityEvents are status events surfaced as activity events.
var activityEvents = map[string]bool{
	"response.file_search_call.in_progress":       true,
	"response.file_search_call.searching":         true,
	"response.file_search_call.completed":         true,
	"response.web_search_call.in_progress":        true,
	"response.web_search_call.searching":          true,
	"response.web_search_call.completed":          true,
	"response.code_interpreter_call.in_progress":  true,
	"response.code_interpreter_call.interpreting": true,
	"response.code_interpreter_call.completed":    true,
	"response.image_generation_call.in_progress":  true,
	"response.image_generation_call.generating":   true,
	"response.image_generation_call.completed":    true,
	"response.mcp_call.in_progress":               true,
	"response.mcp_call.completed":                 true,
	"response.mcp_call.failed":                    true,
	"response.mcp_list_tools.in_progress":         true,
	"response.mcp_list_tools.completed":           true,
	"response.mcp_list_tools.failed":              true,
	"response.compaction.compacting":              true,
}

type usageWire struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type responseWire struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []json.RawMessage `json:"output"`
	Usage  *usageWire        `json:"usage"`
}

type itemWire struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Status           string          `json:"status"`
	Role             string          `json:"role"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        string          `json:"arguments"`
	Input            string          `json:"input"`
	EncryptedContent string          `json:"encrypted_content"`
	ServerLabel      string          `json:"server_label"`
	Output           json.RawMessage `json:"output"`
	Error            json.RawMessage `json:"error"`
}

type wire struct {
	Type            string          `json:"type"`
	SequenceNumber  *int64          `json:"sequence_number"`
	Response        *responseWire   `json:"response"`
	Item            json.RawMessage `json:"item"`
	ItemID          string          `json:"item_id"`
	OutputIndex     int             `json:"output_index"`
	ContentIndex    int             `json:"content_index"`
	SummaryIndex    int             `json:"summary_index"`
	AnnotationIndex int             `json:"annotation_index"`
	Delta           json.RawMessage `json:"delta"`
	Text            string          `json:"text"`
	Refusal         string          `json:"refusal"`
	Arguments       string          `json:"arguments"`
	Input           string          `json:"input"`
	Part            json.RawMessage `json:"part"`
	Annotation      json.RawMessage `json:"annotation"`
	Code            json.RawMessage `json:"code"`
	Message         string          `json:"message"`
	Param           string          `json:"param"`
}

func (w wire) delta() string {
	var s string
	_ = json.Unmarshal(w.Delta, &s)
	return s
}

// tool is a tool_call part in progress, keyed by its output item id.
type tool struct {
	partID   string
	custom   bool
	streamed bool
	ended    bool
	args     strings.Builder
}

type decoder struct {
	*decodekit.Base
	begun     bool
	tools     map[string]*tool
	data      map[string]string // item id -> data part id
	approvals map[string]bool
	order     []string // tool item ids in the order their parts opened
	sawCall   bool
}

func newDecoder(o chatstream.DecodeOptions) *decoder {
	return &decoder{Base: decodekit.New(o), tools: map[string]*tool{}, data: map[string]string{}, approvals: map[string]bool{}}
}

func openaiExt(v map[string]any) map[string]json.RawMessage {
	if len(v) == 0 {
		return nil
	}
	b, _ := json.Marshal(v)
	return map[string]json.RawMessage{"openai": b}
}

func meta(v map[string]any) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for k, x := range v {
		if s, ok := x.(string); ok && s == "" {
			continue
		}
		b, _ := json.Marshal(x)
		m[k] = b
	}
	return m
}

// Decode implements chatstream.Decoder.
func (d *decoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	data := bytes.TrimSpace(f.Data)
	if len(data) == 0 || string(data) == "[DONE]" {
		return nil, nil
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return d.Emit(nil, d.RawEvent(Dialect, "malformed", data)), nil //nolint:nilerr // a bad frame is data, preserved as a raw event
	}
	if w.Type == "" {
		w.Type = f.Event
	}
	if w.Type == "" {
		return d.Emit(nil, d.RawEvent(Dialect, "malformed", data)), nil
	}
	out := d.handle(w, data)
	if w.SequenceNumber != nil {
		out = stampSequence(out, *w.SequenceNumber)
	}
	return out, nil
}

// stampSequence records the upstream sequence_number in Ext["openai"] of every
// event decoded from the frame.
func stampSequence(evs []chatstream.Event, seq int64) []chatstream.Event {
	for i := range evs {
		m := map[string]json.RawMessage{}
		if b, ok := evs[i].Ext["openai"]; ok {
			_ = json.Unmarshal(b, &m)
		}
		sb, _ := json.Marshal(seq)
		m["sequence_number"] = sb
		mb, _ := json.Marshal(m)
		ext := map[string]json.RawMessage{}
		for k, v := range evs[i].Ext {
			ext[k] = v
		}
		ext["openai"] = mb
		evs[i].Ext = ext
	}
	return evs
}

func (d *decoder) begin(out []chatstream.Event, r *responseWire) []chatstream.Event {
	d.begun = true
	start := d.Event(chatstream.VerbRunStart)
	start.Provider, start.Model = d.Options().Provider, d.Options().Model
	if start.Provider == "" {
		start.Provider = "openai"
	}
	ext := map[string]any{}
	if r != nil {
		d.SetRunID(r.ID)
		start.RunID = d.RunID()
		if r.Model != "" {
			start.Model = r.Model
		}
		if r.ID != "" {
			ext["response_id"] = r.ID
		}
	}
	start.Ext = openaiExt(ext)
	out = d.Emit(out, start)
	msg := d.Event(chatstream.VerbMessageStart)
	msg.MessageID, msg.Role = d.RunID(), "assistant"
	return d.Emit(out, msg)
}

func (d *decoder) ensureBegun(out []chatstream.Event, r *responseWire) []chatstream.Event {
	if d.begun {
		return out
	}
	return d.begin(out, r)
}

func (d *decoder) handle(w wire, data []byte) []chatstream.Event {
	var out []chatstream.Event
	switch w.Type {
	case "response.created":
		if d.begun {
			return d.Emit(out, d.RawEvent(Dialect, w.Type, data))
		}
		return d.begin(out, w.Response)
	case "response.queued":
		out = d.ensureBegun(out, w.Response)
		return d.activity(out, "openai.response.queued", map[string]any{})
	case "response.in_progress":
		return d.ensureBegun(out, w.Response)
	case "response.completed", "response.incomplete", "response.failed":
		return d.terminal(w)
	case "error":
		return d.streamError(w, data)
	}
	out = d.ensureBegun(out, nil)
	switch w.Type {
	case "response.output_item.added":
		return d.itemAdded(out, w)
	case "response.output_item.done":
		return d.itemDone(out, w)
	case "response.content_part.added":
		return d.contentPartAdded(out, w)
	case "response.content_part.done":
		return d.endPart(out, contentPartID(w), nil)
	case "response.output_text.delta":
		return d.textDelta(out, contentPartID(w), chatstream.PartText, w, map[string]any{"item_id": w.ItemID, "content_index": w.ContentIndex})
	case "response.output_text.done":
		return d.endPart(out, contentPartID(w), nil)
	case "response.refusal.delta":
		return d.textDelta(out, contentPartID(w), chatstream.PartRefusal, w, map[string]any{"item_id": w.ItemID, "content_index": w.ContentIndex})
	case "response.refusal.done":
		return d.endPart(out, contentPartID(w), nil)
	case "response.reasoning_text.delta":
		return d.textDelta(out, reasoningID(w), chatstream.PartReasoning, w, map[string]any{"item_id": w.ItemID, "content_index": w.ContentIndex, "kind": "full"})
	case "response.reasoning_text.done":
		return d.endPart(out, reasoningID(w), nil)
	case "response.reasoning_summary_part.added":
		return d.openPart(out, summaryID(w), chatstream.PartReasoning, map[string]any{"item_id": w.ItemID, "summary_index": w.SummaryIndex, "kind": "summary"})
	case "response.reasoning_summary_text.delta":
		return d.textDelta(out, summaryID(w), chatstream.PartReasoning, w, map[string]any{"item_id": w.ItemID, "summary_index": w.SummaryIndex, "kind": "summary"})
	case "response.reasoning_summary_text.done", "response.reasoning_summary_part.done":
		return d.endPart(out, summaryID(w), nil)
	case "response.output_text.annotation.added":
		return d.annotation(out, w)
	case "response.function_call_arguments.delta", "response.mcp_call_arguments.delta", "response.custom_tool_call_input.delta":
		return d.toolDelta(out, w, w.delta())
	case "response.function_call_arguments.done", "response.mcp_call_arguments.done":
		return d.toolDone(out, w, w.Arguments, false)
	case "response.custom_tool_call_input.done":
		return d.toolDone(out, w, w.Input, true)
	}
	if activityEvents[w.Type] {
		return d.activity(out, "openai."+strings.TrimPrefix(w.Type, "response."), map[string]any{"item_id": w.ItemID, "output_index": w.OutputIndex})
	}
	// Content this vocabulary does not model (code and shell deltas, partial images,
	// audio) and unknown types alike are preserved raw, never dropped or terminal.
	return d.Emit(out, d.RawEvent(Dialect, w.Type, data))
}

func contentPartID(w wire) string { return fmt.Sprintf("%s/%d", w.ItemID, w.ContentIndex) }
func reasoningID(w wire) string   { return fmt.Sprintf("%s/reasoning/%d", w.ItemID, w.ContentIndex) }
func summaryID(w wire) string     { return fmt.Sprintf("%s/summary/%d", w.ItemID, w.SummaryIndex) }

func (d *decoder) activity(out []chatstream.Event, kind string, value map[string]any) []chatstream.Event {
	ev := d.Event(chatstream.VerbActivity)
	ev.Kind = kind
	ev.Value, _ = json.Marshal(value)
	return d.Emit(out, ev)
}

func (d *decoder) openPart(out []chatstream.Event, id string, k chatstream.PartKind, m map[string]any) []chatstream.Event {
	if d.PartOpen(id) {
		return out
	}
	st := d.Event(chatstream.VerbPartStart)
	st.PartID, st.Kind = id, string(k)
	if mm := meta(m); len(mm) > 0 {
		st.Meta = mm
	}
	return d.Emit(out, st)
}

func (d *decoder) endPart(out []chatstream.Event, id string, final json.RawMessage) []chatstream.Event {
	if !d.PartOpen(id) {
		return out
	}
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID, en.Final = id, final
	return d.Emit(out, en)
}

func (d *decoder) textDelta(out []chatstream.Event, id string, k chatstream.PartKind, w wire, m map[string]any) []chatstream.Event {
	s := w.delta()
	if s == "" {
		return d.openPart(out, id, k, m)
	}
	out = d.openPart(out, id, k, m)
	dl := d.Event(chatstream.VerbPartDelta)
	dl.PartID, dl.Text = id, s
	return d.Emit(out, dl)
}

func (d *decoder) contentPartAdded(out []chatstream.Event, w wire) []chatstream.Event {
	var p struct{ Type string }
	_ = json.Unmarshal(w.Part, &p)
	m := map[string]any{"item_id": w.ItemID, "content_index": w.ContentIndex}
	switch p.Type {
	case "refusal":
		return d.openPart(out, contentPartID(w), chatstream.PartRefusal, m)
	case "reasoning_text":
		m["kind"] = "full"
		return d.openPart(out, reasoningID(w), chatstream.PartReasoning, m)
	default: // output_text, and any content type that is text-like
		return d.openPart(out, contentPartID(w), chatstream.PartText, m)
	}
}

func (d *decoder) annotation(out []chatstream.Event, w wire) []chatstream.Event {
	if len(bytes.TrimSpace(w.Annotation)) == 0 || string(bytes.TrimSpace(w.Annotation)) == "null" {
		return out
	}
	id := fmt.Sprintf("%s/%d/annotation/%d", w.ItemID, w.ContentIndex, w.AnnotationIndex)
	st := d.Event(chatstream.VerbPartStart)
	st.PartID, st.Kind = id, string(chatstream.PartSource)
	st.Meta = map[string]json.RawMessage{"annotation": w.Annotation, "item_id": mustMarshal(w.ItemID)}
	out = d.Emit(out, st)
	return d.endPart(out, id, nil)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (d *decoder) itemAdded(out []chatstream.Event, w wire) []chatstream.Event {
	var it itemWire
	if err := json.Unmarshal(w.Item, &it); err != nil || it.Type == "" {
		return d.Emit(out, d.RawEvent(Dialect, "malformed", w.Item))
	}
	switch it.Type {
	case "message", "reasoning", "mcp_list_tools":
		return out
	case "function_call", "custom_tool_call", "mcp_call":
		out = d.openTool(out, it)
		if it.Arguments != "" {
			out = d.appendTool(out, it.ID, it.Arguments)
		}
		return out
	case "mcp_approval_request":
		return d.approval(out, it)
	}
	return d.openData(out, it, w.OutputIndex)
}

func (d *decoder) openTool(out []chatstream.Event, it itemWire) []chatstream.Event {
	if _, ok := d.tools[it.ID]; ok {
		return out
	}
	id := it.CallID
	if id == "" {
		id = it.ID
	}
	t := &tool{partID: id, custom: it.Type == "custom_tool_call"}
	d.tools[it.ID] = t
	d.order = append(d.order, it.ID)
	if it.Type != "mcp_call" {
		d.sawCall = true
	}
	name := it.Name
	if name == "" {
		name = unknownTool // the item that names it was missed; a tool call must carry a name
	}
	return d.openPart(out, id, chatstream.PartToolCall, map[string]any{
		chatstream.MetaName: name, "call_id": it.CallID, "item_id": it.ID, "type": it.Type, "server_label": it.ServerLabel,
	})
}

func (d *decoder) openData(out []chatstream.Event, it itemWire, idx int) []chatstream.Event {
	id := it.ID
	if id == "" {
		id = fmt.Sprintf("item-%d", idx)
	}
	d.data[it.ID] = id
	return d.openPart(out, id, chatstream.PartData, map[string]any{"type": it.Type, "status": it.Status, "item_id": it.ID})
}

func (d *decoder) approval(out []chatstream.Event, it itemWire) []chatstream.Event {
	if d.approvals[it.ID] {
		return out
	}
	d.approvals[it.ID] = true
	ev := d.Event(chatstream.VerbApprovalRequest)
	ev.ApprovalID, ev.CallID = it.ID, it.CallID
	ev.Reason = "mcp approval requested"
	ev.Mode = chatstream.ApprovalInBand
	ev.Descriptor = mustMarshal(map[string]any{"server_label": it.ServerLabel, "name": it.Name, "arguments": it.Arguments})
	return d.Emit(out, ev)
}

// appendTool records and emits a JSON fragment for the tool item, opening its
// part when the item's added event was missed.
func (d *decoder) appendTool(out []chatstream.Event, itemID, frag string) []chatstream.Event {
	t, ok := d.tools[itemID]
	if !ok {
		out = d.openTool(out, itemWire{ID: itemID, Type: "function_call"})
		t = d.tools[itemID]
	}
	if frag == "" || t.ended {
		return out
	}
	t.streamed = true
	t.args.WriteString(frag)
	dl := d.Event(chatstream.VerbPartDelta)
	dl.PartID, dl.JSONFragment = t.partID, frag
	return d.Emit(out, dl)
}

func (d *decoder) toolDelta(out []chatstream.Event, w wire, frag string) []chatstream.Event {
	return d.appendTool(out, w.ItemID, frag)
}

// toolDone finalizes a tool part from its arguments .done event.
func (d *decoder) toolDone(out []chatstream.Event, w wire, full string, custom bool) []chatstream.Event {
	t, ok := d.tools[w.ItemID]
	if !ok {
		out = d.openTool(out, itemWire{ID: w.ItemID, Type: map[bool]string{true: "custom_tool_call", false: "function_call"}[custom]})
		t = d.tools[w.ItemID]
	}
	return d.finishTool(out, t, full)
}

// finishTool ends a tool part. full is the finished arguments; when nothing
// streamed it is emitted as one fragment so the part carries them.
func (d *decoder) finishTool(out []chatstream.Event, t *tool, full string) []chatstream.Event {
	if t.ended {
		return out
	}
	if full == "" {
		full = t.args.String()
	}
	if !t.streamed && full != "" {
		dl := d.Event(chatstream.VerbPartDelta)
		dl.PartID, dl.JSONFragment = t.partID, full
		out = d.Emit(out, dl)
		t.streamed = true
	}
	t.ended = true
	if !d.PartOpen(t.partID) {
		return out
	}
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID = t.partID
	switch {
	case t.custom && full != "":
		en.Final = mustMarshal(full)
	case full != "" && json.Valid([]byte(full)):
		en.Final = json.RawMessage(full)
	case full != "":
		en.Ext = openaiExt(map[string]any{"arguments_valid": false})
	}
	return d.Emit(out, en)
}

func (d *decoder) itemDone(out []chatstream.Event, w wire) []chatstream.Event {
	var it itemWire
	if err := json.Unmarshal(w.Item, &it); err != nil || it.Type == "" {
		return d.Emit(out, d.RawEvent(Dialect, "malformed", w.Item))
	}
	switch it.Type {
	case "message":
		return out
	case "reasoning":
		if it.EncryptedContent == "" {
			return out
		}
		id := it.ID + "/opaque"
		out = d.openPart(out, id, chatstream.PartReasoning, map[string]any{"item_id": it.ID, "kind": "opaque"})
		return d.endPart(out, id, w.Item)
	case "function_call", "custom_tool_call", "mcp_call":
		out = d.openTool(out, it)
		args := it.Arguments
		if it.Type == "custom_tool_call" {
			args = it.Input
		}
		out = d.finishTool(out, d.tools[it.ID], args)
		if it.Type == "mcp_call" {
			out = d.mcpResult(out, it)
		}
		return out
	case "mcp_approval_request":
		return d.approval(out, it)
	case "mcp_list_tools":
		return d.activity(out, "openai.mcp_list_tools", map[string]any{"item": w.Item})
	}
	if _, ok := d.data[it.ID]; !ok {
		out = d.openData(out, it, w.OutputIndex)
	}
	return d.endPart(out, d.data[it.ID], w.Item)
}

// mcpResult emits the tool_result of a finished mcp_call that carries an output
// or an error.
func (d *decoder) mcpResult(out []chatstream.Event, it itemWire) []chatstream.Event {
	hasOut := len(it.Output) > 0 && string(bytes.TrimSpace(it.Output)) != "null"
	hasErr := len(it.Error) > 0 && string(bytes.TrimSpace(it.Error)) != "null"
	if !hasOut && !hasErr {
		return out
	}
	id := d.tools[it.ID].partID + "/result"
	out = d.openPart(out, id, chatstream.PartToolResult, map[string]any{chatstream.MetaCallID: d.tools[it.ID].partID, "item_id": it.ID, chatstream.MetaIsError: hasErr})
	res := map[string]json.RawMessage{}
	if hasOut {
		res["output"] = it.Output
	}
	if hasErr {
		res["error"] = it.Error
	}
	return d.endPart(out, id, mustMarshal(res))
}

// closeAll ends every open part and the message. Tool parts still open get
// their accumulated arguments as Final when those are valid JSON.
func (d *decoder) closeAll(out []chatstream.Event) []chatstream.Event {
	for _, id := range d.order {
		if t := d.tools[id]; !t.ended {
			out = d.finishTool(out, t, "")
		}
	}
	return d.Unwind(out)
}

func (d *decoder) usage(r *responseWire) (*chatstream.Usage, bool) {
	if r == nil || r.Usage == nil {
		return nil, false
	}
	u, err := chatstream.UsageFromInclusive(chatstream.UsageFinal,
		r.Usage.InputTokens, r.Usage.InputTokensDetails.CachedTokens, r.Usage.InputTokensDetails.CacheWriteTokens,
		r.Usage.OutputTokens, r.Usage.OutputTokensDetails.ReasoningTokens)
	if err != nil {
		return nil, true
	}
	return &u, false
}

// terminal handles response.completed, response.incomplete and response.failed.
func (d *decoder) terminal(w wire) []chatstream.Event {
	r := w.Response
	if r == nil {
		r = &responseWire{}
	}
	out := d.ensureBegun(nil, r)
	out = d.closeAll(out)
	u, inconsistent := d.usage(r)
	if inconsistent {
		out = d.Emit(out, d.RawEvent(Dialect, "usage_inconsistent", mustMarshal(r.Usage)))
	}
	ext := map[string]any{"status": r.Status}
	if r.ID != "" {
		ext["response_id"] = r.ID
	}
	failed := w.Type == "response.failed" || r.Status == "failed" || (r.Error != nil && (r.Error.Code != "" || r.Error.Message != ""))
	switch {
	case failed:
		if u != nil {
			ev := d.Event(chatstream.VerbUsage)
			ev.Usage = u
			out = d.Emit(out, ev)
		}
		ev := d.Event(chatstream.VerbRunError)
		ev.Code, ev.Message = chatstream.CodeUpstreamError, "response failed"
		if r.Error != nil {
			if r.Error.Code != "" {
				ev.Code = r.Error.Code
			}
			if r.Error.Message != "" {
				ev.Message = r.Error.Message
			}
		}
		ev.Retryable = retryableCodes[ev.Code]
		ev.Ext = openaiExt(ext)
		return d.Emit(out, ev)
	case r.Status == "cancelled": //nolint:misspell // the upstream status value
		if u != nil {
			ev := d.Event(chatstream.VerbUsage)
			ev.Usage = u
			out = d.Emit(out, ev)
		}
		ev := d.Event(chatstream.VerbRunAbort)
		ev.Reason = "cancelled" //nolint:misspell // the upstream status value
		ev.Ext = openaiExt(ext)
		return d.Emit(out, ev)
	}
	fin := d.Event(chatstream.VerbRunFinish)
	fin.Usage = u
	if w.Type == "response.incomplete" || r.Status == "incomplete" {
		raw := ""
		if r.IncompleteDetails != nil {
			raw = r.IncompleteDetails.Reason
		}
		fin.RawReason = raw
		fin.Reason = string(chatstream.FinishOther)
		if reason, ok := incompleteReasons[raw]; ok {
			fin.Reason = string(reason)
		}
	} else {
		fin.RawReason = "completed"
		fin.Reason = string(chatstream.FinishStop)
		if d.outputHasCall(r) {
			fin.Reason = string(chatstream.FinishToolCalls)
		}
	}
	fin.Ext = openaiExt(ext)
	return d.Emit(out, fin)
}

// outputHasCall reports whether the finished response asked for a tool run: its
// output lists a function_call or custom_tool_call, or (when the final output
// is absent) one was seen streaming.
func (d *decoder) outputHasCall(r *responseWire) bool {
	if len(r.Output) == 0 {
		return d.sawCall
	}
	for _, raw := range r.Output {
		var it struct{ Type string }
		_ = json.Unmarshal(raw, &it)
		if it.Type == "function_call" || it.Type == "custom_tool_call" {
			return true
		}
	}
	return false
}

// streamError handles the standalone error event.
func (d *decoder) streamError(w wire, data []byte) []chatstream.Event {
	code := strings.Trim(strings.TrimSpace(string(w.Code)), `"`)
	if code == "" || code == "null" {
		code = chatstream.CodeUpstreamError
	}
	out := d.ensureBegun(nil, nil)
	out = d.closeAll(out)
	ev := d.Event(chatstream.VerbRunError)
	ev.Code, ev.Message = code, w.Message
	ev.Retryable = retryableCodes[code]
	ev.Raw = &chatstream.Raw{Dialect: Dialect, Type: "error", Payload: json.RawMessage(data)}
	if w.Param != "" {
		ev.Ext = openaiExt(map[string]any{"param": w.Param})
	}
	return d.Emit(out, ev)
}

// Close implements chatstream.Decoder. Without one of the terminal events the
// stream is truncated, whatever else arrived.
func (d *decoder) Close(cause error) []chatstream.Event {
	if d.Closed() || d.Terminated() {
		return d.Base.Close(cause)
	}
	out := d.ensureBegun(nil, nil)
	out = d.closeAll(out)
	return append(out, d.Base.Close(cause)...)
}
