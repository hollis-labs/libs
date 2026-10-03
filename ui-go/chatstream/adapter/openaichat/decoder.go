package openaichat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/internal/decodekit"
)

// finishReasons maps chat.completion finish_reason to the closed vocabulary.
var finishReasons = map[string]chatstream.FinishReason{
	"stop":           chatstream.FinishStop,
	"length":         chatstream.FinishLength,
	"content_filter": chatstream.FinishContentFilter,
	"tool_calls":     chatstream.FinishToolCalls,
	"function_call":  chatstream.FinishToolCalls,
}

// retryableErrors are error types and codes a client should retry.
var retryableErrors = map[string]bool{
	"rate_limit_exceeded": true, "rate_limit_error": true, "requests": true, "tokens": true,
	"server_error": true, "service_unavailable": true, "api_error": true, "timeout": true,
	"overloaded": true, "overloaded_error": true, "engine_overloaded_error": true,
	"429": true, "500": true, "502": true, "503": true, "504": true,
}

var (
	chunkKeys  = keySet("id", "object", "created", "model", "system_fingerprint", "service_tier", "obfuscation", "choices", "usage", "moderation", "error")
	choiceKeys = keySet("index", "delta", "finish_reason", "logprobs")
	deltaKeys  = keySet("role", "content", "refusal", "tool_calls", "function_call", "reasoning_content", "reasoning")
)

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// MaxToolCalls is the most tool calls (distinct tool_calls indices) the decoder
// keeps open at once. A stream that names more ends with a non-retryable
// run.error, chatstream.CodeLimitExceeded, instead of growing without bound; a
// real response has a handful.
const MaxToolCalls = 1024

type toolState struct {
	partID  string
	name    string
	opened  bool
	pending strings.Builder // argument fragments received before the name
	args    strings.Builder
}

type decoder struct {
	*decodekit.Base
	begun     bool
	counters  map[chatstream.PartKind]int
	content   string // id of the open text/refusal/reasoning part
	contentK  chatstream.PartKind
	tools     map[int]*toolState
	toolOrder []int // indexes in the order their calls began
	finish    string
	finishSet bool
	usage     *chatstream.Usage
}

func newDecoder(o chatstream.DecodeOptions) *decoder {
	return &decoder{Base: decodekit.New(o), counters: map[chatstream.PartKind]int{}, tools: map[int]*toolState{}}
}

func openaiExt(v map[string]any) map[string]json.RawMessage {
	if len(v) == 0 {
		return nil
	}
	b, _ := json.Marshal(v)
	return map[string]json.RawMessage{"openai": b}
}

// Decode implements chatstream.Decoder.
func (d *decoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if err := d.CheckOpen(); err != nil {
		return nil, err
	}
	data := bytes.TrimSpace(f.Data)
	if len(data) == 0 {
		return nil, nil
	}
	if string(data) == "[DONE]" {
		return d.done(), nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return d.Emit(nil, d.RawEvent(Dialect, "malformed", data)), nil //nolint:nilerr // a bad frame is data, preserved as a raw event
	}
	if e, ok := top["error"]; ok && string(bytes.TrimSpace(e)) != "null" {
		return d.fail(e, data), nil
	}
	var c struct {
		ID, Model         string
		Created           int64
		SystemFingerprint string `json:"system_fingerprint"`
		ServiceTier       string `json:"service_tier"`
		Choices           []json.RawMessage
		Usage             *usageWire
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return d.Emit(nil, d.RawEvent(Dialect, "malformed", data)), nil //nolint:nilerr // a bad frame is data, preserved as a raw event
	}
	unknown := map[string]json.RawMessage{}
	for k, v := range top {
		if !chunkKeys[k] {
			unknown[k] = v
		}
	}

	var out []chatstream.Event
	if !d.begun {
		out = d.begin(out, c.ID, c.Model, c.Created, c.SystemFingerprint, c.ServiceTier)
	}
	if m, ok := top["moderation"]; ok && string(bytes.TrimSpace(m)) != "null" {
		out = d.Emit(out, d.RawEvent(Dialect, "moderation", m))
	}
	for _, rawChoice := range c.Choices {
		out = d.choice(out, rawChoice, unknown)
	}
	if c.Usage != nil {
		u, err := c.Usage.convert()
		if err != nil {
			out = d.Emit(out, d.RawEvent(Dialect, "usage_inconsistent", mustJSON(top["usage"])))
		} else {
			d.usage = &u
		}
	}
	if len(unknown) > 0 {
		b, _ := json.Marshal(unknown)
		out = d.Emit(out, d.RawEvent(Dialect, "unknown_fields", b))
	}
	return out, nil
}

func mustJSON(b json.RawMessage) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

// ensureBegun brackets a stream that ends (or fails) before any chunk arrived.
func (d *decoder) ensureBegun(out []chatstream.Event) []chatstream.Event {
	if d.begun {
		return out
	}
	return d.begin(out, "", "", 0, "", "")
}

func (d *decoder) begin(out []chatstream.Event, id, model string, created int64, fingerprint, tier string) []chatstream.Event {
	d.begun = true
	d.SetRunID(id)
	start := d.Event(chatstream.VerbRunStart)
	start.Provider, start.Model = d.Options().Provider, d.Options().Model
	if start.Provider == "" {
		start.Provider = "openai"
	}
	if model != "" {
		start.Model = model
	}
	ext := map[string]any{}
	if id != "" {
		ext["id"] = id
	}
	if created != 0 {
		ext["created"] = created
	}
	if fingerprint != "" {
		ext["system_fingerprint"] = fingerprint
	}
	if tier != "" {
		ext["service_tier"] = tier
	}
	start.Ext = openaiExt(ext)
	out = d.Emit(out, start)
	msg := d.Event(chatstream.VerbMessageStart)
	msg.MessageID, msg.Role = id, "assistant"
	if msg.MessageID == "" {
		msg.MessageID = d.RunID()
	}
	return d.Emit(out, msg)
}

func (d *decoder) choice(out []chatstream.Event, raw json.RawMessage, unknown map[string]json.RawMessage) []chatstream.Event {
	var ch map[string]json.RawMessage
	if json.Unmarshal(raw, &ch) != nil {
		return d.Emit(out, d.RawEvent(Dialect, "malformed", raw))
	}
	var idx int
	if v, ok := ch["index"]; ok {
		_ = json.Unmarshal(v, &idx)
	}
	if idx != 0 {
		return d.Emit(out, d.RawEvent(Dialect, "choice_ignored", raw))
	}
	for k, v := range ch {
		if !choiceKeys[k] {
			unknown["choices[0]."+k] = v
		}
	}
	var delta map[string]json.RawMessage
	if v, ok := ch["delta"]; ok && string(bytes.TrimSpace(v)) != "null" {
		if json.Unmarshal(v, &delta) != nil {
			return d.Emit(out, d.RawEvent(Dialect, "malformed", raw))
		}
	}
	for k, v := range delta {
		if !deltaKeys[k] {
			unknown["delta."+k] = v
		}
	}
	str := func(key string) string {
		var s string
		_ = json.Unmarshal(delta[key], &s)
		return s
	}
	out = d.appendContent(out, chatstream.PartReasoning, str("reasoning_content")+str("reasoning"), map[string]any{"field": reasoningField(delta)})
	out = d.appendContent(out, chatstream.PartText, str("content"), nil)
	out = d.appendContent(out, chatstream.PartRefusal, str("refusal"), nil)
	if v, ok := delta["tool_calls"]; ok {
		var tcs []struct {
			Index    int    `json:"index"`
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		}
		if json.Unmarshal(v, &tcs) != nil {
			out = d.Emit(out, d.RawEvent(Dialect, "malformed", v))
		}
		for _, tc := range tcs {
			out = d.toolFragment(out, tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
	}
	if v, ok := delta["function_call"]; ok && string(bytes.TrimSpace(v)) != "null" {
		var fc struct{ Name, Arguments string }
		if json.Unmarshal(v, &fc) == nil {
			out = d.toolFragment(out, -1, "function_call", fc.Name, fc.Arguments)
		}
	}
	if v, ok := ch["finish_reason"]; ok {
		var fr *string
		// "" is not a finish: some servers send finish_reason "" on every chunk.
		if json.Unmarshal(v, &fr) == nil && fr != nil && *fr != "" {
			d.finish, d.finishSet = *fr, true
			out = d.closeAll(out)
		}
	}
	return out
}

func reasoningField(delta map[string]json.RawMessage) string {
	if _, ok := delta["reasoning_content"]; ok {
		return "reasoning_content"
	}
	return "reasoning"
}

// appendContent appends s to the open text, refusal or reasoning part of kind k,
// opening it (and closing a different open content part) when needed.
func (d *decoder) appendContent(out []chatstream.Event, k chatstream.PartKind, s string, meta map[string]any) []chatstream.Event {
	if s == "" {
		return out
	}
	if d.content != "" && d.contentK != k {
		out = d.endContent(out)
	}
	if d.content == "" {
		d.counters[k]++
		d.content, d.contentK = fmt.Sprintf("%s-%d", k, d.counters[k]), k
		st := d.Event(chatstream.VerbPartStart)
		st.PartID, st.Kind = d.content, string(k)
		if len(meta) > 0 {
			st.Meta = map[string]json.RawMessage{}
			for mk, mv := range meta {
				b, _ := json.Marshal(mv)
				st.Meta[mk] = b
			}
		}
		out = d.Emit(out, st)
	}
	dl := d.Event(chatstream.VerbPartDelta)
	dl.PartID, dl.Text = d.content, s
	return d.Emit(out, dl)
}

func (d *decoder) endContent(out []chatstream.Event) []chatstream.Event {
	if d.content == "" {
		return out
	}
	en := d.Event(chatstream.VerbPartEnd)
	en.PartID = d.content
	d.content = ""
	return d.Emit(out, en)
}

// unknownTool names a tool call that never carried a name.
const unknownTool = "unknown"

// toolFragment feeds one tool-call fragment. The first fragment of a call
// carries its name per the reference; a server that sends the id first and the
// name later has its arguments held until the name arrives, because part.start
// must carry the name. A call that ends with no name at all is opened as
// "unknown" with Ext["openai"]["name_missing"].
func (d *decoder) toolFragment(out []chatstream.Event, idx int, id, name, args string) []chatstream.Event {
	if d.Terminated() {
		return out
	}
	st := d.tools[idx]
	if st == nil {
		if len(d.tools) >= MaxToolCalls {
			out = d.endContent(out)
			out = d.endTools(out)
			return d.LimitExceeded(out, "open tool calls", MaxToolCalls)
		}
		st = &toolState{partID: id}
		if st.partID == "" {
			st.partID = fmt.Sprintf("call-%d", idx)
		}
		d.tools[idx] = st
		d.toolOrder = append(d.toolOrder, idx)
	}
	if st.name == "" && name != "" {
		st.name = name
	}
	if args != "" {
		if st.opened {
			return d.toolDelta(out, st, args)
		}
		st.pending.WriteString(args)
	}
	if !st.opened && st.name != "" {
		out = d.openTool(out, idx, st)
	}
	return out
}

func (d *decoder) openTool(out []chatstream.Event, idx int, st *toolState) []chatstream.Event {
	out = d.endContent(out)
	st.opened = true
	start := d.Event(chatstream.VerbPartStart)
	start.PartID, start.Kind = st.partID, string(chatstream.PartToolCall)
	name := st.name
	if name == "" {
		name = unknownTool
	}
	start.Meta = map[string]json.RawMessage{}
	start.Meta[chatstream.MetaName], _ = json.Marshal(name)
	start.Meta["index"], _ = json.Marshal(idx)
	if st.name == "" {
		start.Ext = openaiExt(map[string]any{"name_missing": true})
	}
	out = d.Emit(out, start)
	if p := st.pending.String(); p != "" {
		st.pending.Reset()
		out = d.toolDelta(out, st, p)
	}
	return out
}

func (d *decoder) toolDelta(out []chatstream.Event, st *toolState, frag string) []chatstream.Event {
	st.args.WriteString(frag)
	dl := d.Event(chatstream.VerbPartDelta)
	dl.PartID, dl.JSONFragment = st.partID, frag
	return d.Emit(out, dl)
}

func (d *decoder) endTools(out []chatstream.Event) []chatstream.Event {
	for _, i := range d.toolOrder {
		st := d.tools[i]
		if !st.opened {
			out = d.openTool(out, i, st)
		}
		en := d.Event(chatstream.VerbPartEnd)
		en.PartID = st.partID
		if args := st.args.String(); args != "" {
			if json.Valid([]byte(args)) {
				en.Final = json.RawMessage(args)
			} else {
				en.Ext = openaiExt(map[string]any{"arguments_valid": false})
			}
		}
		out = d.Emit(out, en)
	}
	d.tools, d.toolOrder = map[int]*toolState{}, nil
	return out
}

// closeAll closes everything a finish_reason (or the end of the stream) ends:
// the content part, every tool call, then the message.
func (d *decoder) closeAll(out []chatstream.Event) []chatstream.Event {
	out = d.endContent(out)
	out = d.endTools(out)
	return d.Unwind(out)
}

func (d *decoder) usageEvent(out []chatstream.Event) []chatstream.Event {
	if d.usage == nil {
		return out
	}
	u := *d.usage
	ev := d.Event(chatstream.VerbUsage)
	ev.Usage = &u
	d.usage = nil
	return d.Emit(out, ev)
}

// done handles [DONE], the dialect's terminal signal.
func (d *decoder) done() []chatstream.Event {
	out := d.ensureBegun(nil)
	out = d.closeAll(out)
	fin := d.Event(chatstream.VerbRunFinish)
	fin.Reason, fin.RawReason = string(chatstream.FinishOther), d.finish
	if r, ok := finishReasons[d.finish]; ok {
		fin.Reason = string(r)
	}
	if d.usage != nil {
		u := *d.usage
		fin.Usage = &u
	}
	return d.Emit(out, fin)
}

// fail handles an {"error": ...} frame.
func (d *decoder) fail(raw json.RawMessage, frame []byte) []chatstream.Event {
	// The error frame is terminal, so its fields are read leniently: a message
	// that is an object or a param that is a number must not lose the message or
	// the classification.
	var w struct {
		Message json.RawMessage `json:"message"`
		Type    json.RawMessage `json:"type"`
		Code    json.RawMessage `json:"code"`
		Param   json.RawMessage `json:"param"`
	}
	var e struct {
		Message, Type, Param string
		Code                 json.RawMessage
	}
	if json.Unmarshal(raw, &w) != nil {
		// error is a bare string (some compatible servers)
		e.Message = lenientText(raw)
	} else {
		e.Message, e.Type, e.Param, e.Code = lenientText(w.Message), lenientText(w.Type), lenientText(w.Param), w.Code
	}
	code := strings.Trim(strings.TrimSpace(string(e.Code)), `"`)
	if code == "null" {
		code = ""
	}
	if code == "" {
		code = e.Type
	}
	if code == "" {
		code = chatstream.CodeUpstreamError
	}
	out := d.ensureBegun(nil)
	out = d.closeAll(out)
	out = d.usageEvent(out)
	ev := d.Event(chatstream.VerbRunError)
	ev.Code, ev.Message = code, e.Message
	ev.Retryable = retryableErrors[code] || retryableErrors[e.Type]
	ev.Raw = &chatstream.Raw{Dialect: Dialect, Type: "error", Payload: json.RawMessage(frame)}
	ext := map[string]any{}
	if e.Type != "" {
		ext["type"] = e.Type
	}
	if e.Param != "" {
		ext["param"] = e.Param
	}
	ev.Ext = openaiExt(ext)
	return d.Emit(out, ev)
}

// lenientText is a JSON string's value, else the JSON's own text ("" for absent
// or null).
func lenientText(raw json.RawMessage) string {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 || string(s) == "null" {
		return ""
	}
	var str string
	if json.Unmarshal(s, &str) == nil {
		return str
	}
	var buf bytes.Buffer
	if json.Compact(&buf, s) == nil {
		return buf.String()
	}
	return string(s)
}

// Close implements chatstream.Decoder. See the package documentation for why a
// stream that reached finish_reason but not [DONE] is still truncated.
func (d *decoder) Close(cause error) []chatstream.Event {
	if d.Closed() || d.Terminated() {
		return d.Base.Close(cause)
	}
	out := d.ensureBegun(nil)
	out = d.closeAll(out)
	out = d.usageEvent(out)
	tail := d.Base.Close(cause)
	for i := range tail {
		if tail[i].Verb == chatstream.VerbRunError && d.finishSet {
			tail[i].Ext = openaiExt(map[string]any{"finish_reason_seen": d.finish})
		}
	}
	return append(out, tail...)
}

type usageWire struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
		AudioTokens      int `json:"audio_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens          int `json:"reasoning_tokens"`
		AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
		RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
		AudioTokens              int `json:"audio_tokens"`
	} `json:"completion_tokens_details"`
}

// convert turns the inclusive counts into disjoint components. Predicted-output
// and audio counters go to Extra: they are provider counters, never in Total.
func (w usageWire) convert() (chatstream.Usage, error) {
	u, err := chatstream.UsageFromInclusive(chatstream.UsageFinal,
		w.PromptTokens, w.PromptTokensDetails.CachedTokens, w.PromptTokensDetails.CacheWriteTokens,
		w.CompletionTokens, w.CompletionTokensDetails.ReasoningTokens)
	if err != nil {
		return chatstream.Usage{}, err
	}
	extra := map[string]int{
		"accepted_prediction_tokens": w.CompletionTokensDetails.AcceptedPredictionTokens,
		"rejected_prediction_tokens": w.CompletionTokensDetails.RejectedPredictionTokens,
		"prompt_audio_tokens":        w.PromptTokensDetails.AudioTokens,
		"completion_audio_tokens":    w.CompletionTokensDetails.AudioTokens,
	}
	for k, v := range extra {
		if v != 0 {
			if u.Extra == nil {
				u.Extra = map[string]int{}
			}
			u.Extra[k] = v
		}
	}
	return u, nil
}
