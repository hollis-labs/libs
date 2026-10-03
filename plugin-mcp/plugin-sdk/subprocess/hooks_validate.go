package subprocess

import (
	"bytes"
	"encoding/json"
	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
	"regexp"
	"sort"
)

var hookNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
var hookTracePattern = regexp.MustCompile(`^[0-9a-f]+$`)
var hookFailureCodes = []string{"remote_not_allowed", "latency_budget_exceeded", "schema_mismatch", "profile_unavailable", "stale_scope", "stale_binding", "capacity_exhausted", "deadline_exceeded", "caller_cancelled", "depth_exceeded", "callback_cycle", "transport_failure", "handler_panic", "invalid_output", "handler_error"}

func hookMarshal(v any) ([]byte, error) {
	if err := payloadResultSource(v); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}

// The standard encoder compacts RawMessage, even from MarshalJSON. Named hook
// codecs splice the validated payload token so its internal whitespace, escaped
// keys, number spelling and HTML characters survive exactly.
func hookPreservePayload(v any, payload json.RawMessage) ([]byte, error) {
	raw, err := hookMarshal(v)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	fields["payload"] = payload
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		out.Write(key)
		out.WriteByte(':')
		out.Write(fields[k])
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}
func hookBatchJSON[T interface{ MarshalJSON() ([]byte, error) }](items []T) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString(`{"items":[`)
	for i, item := range items {
		if i > 0 {
			out.WriteByte(',')
		}
		raw, err := item.MarshalJSON()
		if err != nil {
			return nil, err
		}
		out.Write(raw)
	}
	out.WriteString(`]}`)
	return out.Bytes(), nil
}

// ValidateHookDTO validates raw tokens before decoding erases field presence,
// duplicates, integer form or exact casing. Payload is opaque and may be null.
func ValidateHookDTO(name string, raw []byte) error {
	if len(raw) > MaxHookDTOBytes || strictjson.Validate(raw) != nil || payloadFiniteNumbers(raw) != nil {
		return hookInvalid(name, "invalid or oversized JSON")
	}
	token := hostRPCString(256, true)
	fields := map[string]hostRPCRule{}
	optional := []string{}
	switch name {
	case "HookScope":
		fields = map[string]hostRPCRule{"incarnation": func(b json.RawMessage, f string) error {
			var v capability.RuntimeIdentity
			if json.Unmarshal(b, &v) != nil {
				return hookInvalid(f, "invalid incarnation")
			}
			return nil
		}, "registration_id": token}
	case "HookTrace":
		trace := func(size int) hostRPCRule {
			return func(b json.RawMessage, f string) error {
				var s string
				if json.Unmarshal(b, &s) != nil || len(s) != size || !hookTracePattern.MatchString(s) || s == string(bytes.Repeat([]byte{'0'}, size)) {
					return hookInvalid(f, "invalid trace identifier")
				}
				return nil
			}
		}
		fields = map[string]hostRPCRule{"trace_id": trace(32), "span_id": trace(16), "parent_span_id": trace(16)}
		optional = []string{"parent_span_id"}
	case "HookFailure":
		fields = map[string]hostRPCRule{"code": hostRPCEnum(hookFailureCodes...), "message": hostRPCString(16384, false)}
		optional = []string{"message"}
	case "HookErrorData":
		fields = map[string]hostRPCRule{"contract": hostRPCEnum("hooks/1"), "code": hostRPCEnum("invalid_params", "profile_unavailable", "invalid_request", "parse_error", "method_not_found"), "field": hostRPCString(256, true)}
		optional = []string{"field"}
	case "HookHandleParams":
		nameRule := func(b json.RawMessage, f string) error {
			if err := token(b, f); err != nil {
				return err
			}
			var s string
			_ = json.Unmarshal(b, &s)
			if !hookNamePattern.MatchString(s) {
				return hookInvalid(f, "invalid hook name")
			}
			return nil
		}
		fields = map[string]hostRPCRule{"invocation_id": token, "catalog_version": token, "hook": nameRule, "schema_digest": token, "kind": hostRPCEnum("action", "filter"), "mode": hostRPCEnum("sequential", "parallel", "bail", "waterfall", "async", "after_commit"), "scope": hookRef("HookScope"), "context": func(b json.RawMessage, f string) error { return ValidateHostRPCDTO("ForwardContext", b) }, "payload": func(b json.RawMessage, f string) error { return strictjson.Validate(b) }, "metadata": func(b json.RawMessage, f string) error { return payloadStrings(b) }, "deadline": hostRPCShapes["Timestamp"], "aggregate_budget_ms": hostRPCInteger(1, 4294967295), "depth": hostRPCInteger(1, 4294967295), "trace": hookRef("HookTrace"), "root_invocation_id": token, "parent_invocation_id": token}
		optional = []string{"parent_invocation_id"}
	case "HookHandleResult":
		fields = map[string]hostRPCRule{"invocation_id": token, "status": hostRPCEnum("ok", "cancelled", "approval_required", "failed", "unavailable"), "payload": func(b json.RawMessage, f string) error { return strictjson.Validate(b) }, "reason": hostRPCString(16384, false), "error": hookRef("HookFailure")}
		optional = []string{"payload", "reason", "error"}
	case "HookHandleBatchParams", "HookHandleBatchResult":
		child := "HookHandleParams"
		if name == "HookHandleBatchResult" {
			child = "HookHandleResult"
		}
		fields = map[string]hostRPCRule{"items": func(b json.RawMessage, f string) error {
			var items []json.RawMessage
			if json.Unmarshal(b, &items) != nil || len(items) < 1 || len(items) > MaxHookBatchItems {
				return hookInvalid(f, "batch requires 1..64 items")
			}
			seen := map[string]bool{}
			for _, item := range items {
				if err := ValidateHookDTO(child, item); err != nil {
					return err
				}
				var v struct {
					InvocationID string `json:"invocation_id"`
				}
				_ = json.Unmarshal(item, &v)
				if seen[v.InvocationID] {
					return hookInvalid(f, "duplicate invocation_id")
				}
				seen[v.InvocationID] = true
			}
			return nil
		}}
	default:
		return hookInvalid(name, "unknown DTO")
	}
	if err := hostRPCObject(fields, optional...)(raw, name); err != nil {
		if v, ok := err.(*HookValidationError); ok {
			return v
		}
		if v, ok := err.(*HostRPCValidationError); ok {
			return hookInvalid(v.Field, v.Reason)
		}
		return hookInvalid(name, "invalid field")
	}
	var v map[string]json.RawMessage
	_ = json.Unmarshal(raw, &v)
	if name == "HookHandleParams" {
		var kind, mode string
		_ = json.Unmarshal(v["kind"], &kind)
		_ = json.Unmarshal(v["mode"], &mode)
		if (kind == "filter") != (mode == "waterfall") {
			return hookInvalid("mode", "kind/mode mismatch")
		}
	}
	if name == "HookHandleResult" {
		var status string
		_ = json.Unmarshal(v["status"], &status)
		_, payload := v["payload"]
		_, reason := v["reason"]
		_, failure := v["error"]
		switch status {
		case "ok":
			if reason || failure {
				return hookInvalid("status", "contradictory result branch")
			}
		case "cancelled", "approval_required":
			if payload || failure {
				return hookInvalid("status", "contradictory veto branch")
			}
		case "failed", "unavailable":
			if payload || reason || !failure {
				return hookInvalid("status", "failure requires error only")
			}
		}
	}
	return nil
}
func hookRef(name string) hostRPCRule {
	return func(b json.RawMessage, f string) error { return ValidateHookDTO(name, b) }
}

// ValidateHookRequest selects request or notification semantics explicitly.
// Callers must derive notification from ABSENT id, never null or zero.
func ValidateHookRequest(p HookHandleParams, notification bool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if notification {
		if p.Kind != "action" || (p.Mode != "async" && p.Mode != "after_commit") {
			return hookInvalid("mode", "illegal notification")
		}
	} else if p.Context.BindingID == nil {
		return hookInvalid("context.binding_id", "required for request")
	}
	return nil
}
func ValidateHookBatchRequest(p HookHandleBatchParams, notification bool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for _, item := range p.Items {
		if err := ValidateHookRequest(item, notification); err != nil {
			return err
		}
		if item.Kind != "action" || item.Mode == "bail" {
			return hookInvalid("items", "batch requires observation actions")
		}
	}
	return nil
}
func ValidateHookResultFor(p HookHandleParams, r HookHandleResult) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.InvocationID != p.InvocationID {
		return hookInvalid("invocation_id", "result correlation mismatch")
	}
	if r.Status == "ok" && (p.Kind == "filter") != (len(r.Payload) > 0) {
		return hookInvalid("payload", "kind/output mismatch")
	}
	if (r.Status == "cancelled" || r.Status == "approval_required") && (p.Kind != "action" || p.Mode != "bail") {
		return hookInvalid("status", "veto requires bail action")
	}
	return nil
}
func ValidateHookBatchResultFor(p HookHandleBatchParams, r HookHandleBatchResult) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if len(p.Items) != len(r.Items) {
		return hookInvalid("items", "result count mismatch")
	}
	for i := range p.Items {
		if err := ValidateHookResultFor(p.Items[i], r.Items[i]); err != nil {
			return err
		}
	}
	return nil
}
