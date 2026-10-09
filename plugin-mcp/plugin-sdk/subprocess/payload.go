package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/internal/strictjson"
)

// ForwardContextFromContext returns host-provided invocation metadata. It does
// not authorize a binding or enforce a deadline; those remain host policy.
func ForwardContextFromContext(ctx context.Context) (ForwardContext, bool) {
	v, ok := ctx.Value(forwardContextKey{}).(ForwardContext)
	return v, ok
}

type forwardContextKey struct{}

func withForwardContext(ctx context.Context, v *ForwardContext) context.Context {
	if v == nil {
		return ctx
	}
	return context.WithValue(ctx, forwardContextKey{}, *v)
}

type payloadRule func(json.RawMessage) error

func payloadString(nonblank bool) payloadRule {
	return func(raw json.RawMessage) error {
		var v *string
		if json.Unmarshal(raw, &v) != nil || v == nil || nonblank && strings.TrimFunc(*v, hostRPCSpace) == "" {
			return fmt.Errorf("expected string")
		}
		return nil
	}
}
func payloadObject(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return fmt.Errorf("expected object")
	}
	return nil
}
func payloadBool(raw json.RawMessage) error {
	var v *bool
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return fmt.Errorf("expected boolean")
	}
	return nil
}
func payloadStrings(raw json.RawMessage) error {
	if err := payloadObject(raw); err != nil {
		return err
	}
	var f map[string]json.RawMessage
	_ = json.Unmarshal(raw, &f)
	for _, v := range f {
		if err := payloadString(false)(v); err != nil {
			return err
		}
	}
	return nil
}
func payloadJSON(raw json.RawMessage) error    { return nil } // syntax/duplicates checked before field rules
func payloadContext(raw json.RawMessage) error { return ValidateHostRPCDTO("ForwardContext", raw) }
func payloadBase64(raw json.RawMessage) error  { return hostRPCBase64(raw, "body") }
func payloadFields(raw []byte, rules map[string]payloadRule, required []string, nullable ...string) (map[string]json.RawMessage, error) {
	if err := strictjson.Validate(raw); err != nil {
		return nil, fmt.Errorf("invalid or ambiguous JSON")
	}
	if err := payloadObject(raw); err != nil {
		return nil, err
	}
	var f map[string]json.RawMessage
	_ = json.Unmarshal(raw, &f)
	for _, k := range required {
		if _, ok := f[k]; !ok {
			return nil, fmt.Errorf("required field %s", k)
		}
	}
	for k, v := range f {
		rule, ok := rules[k]
		if !ok {
			return nil, fmt.Errorf("unknown or incorrectly cased field")
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			permitted := false
			for _, n := range nullable {
				if k == n {
					permitted = true
				}
			}
			if permitted {
				continue
			}
			return nil, fmt.Errorf("null field %s", k)
		}
		if err := rule(v); err != nil {
			return nil, fmt.Errorf("invalid field %s", k)
		}
	}
	return f, nil
}
func paramsJSON(raw any) ([]byte, error) {
	if b, ok := raw.(json.RawMessage); ok {
		return b, nil
	}
	return json.Marshal(raw)
}

// Inspect raw members before struct decoding can erase presence or accept case aliases.
func validateRuntimeParams(method string, raw any) (*ForwardContext, error) {
	rules := map[string]payloadRule{"context": payloadContext}
	var required []string
	add := func(kind payloadRule, names ...string) {
		for _, n := range names {
			rules[n] = kind
		}
	}
	switch method {
	case MethodLoad, MethodUnload, MethodHealth:
		if raw == nil {
			return nil, nil
		}
	case MethodCommandExecute:
		required = []string{"name", "session_id", "args"}
		add(payloadString(true), "name")
		add(payloadString(false), "session_id", "args")
		add(payloadJSON, "identity")
	case MethodEventHandle:
		required = []string{"type", "source", "data", "pre_hook"}
		add(payloadString(true), "type", "source")
		add(payloadString(false), "session_id")
		add(payloadObject, "data")
		add(payloadBool, "pre_hook")
		add(payloadJSON, "identity")
	case MethodCRUDCreate, MethodCRUDRead, MethodCRUDUpdate, MethodCRUDDelete, MethodCRUDList:
		required = []string{"resource_type"}
		add(payloadString(true), "resource_type", "id")
		add(payloadObject, "data", "filters")
		if method == MethodCRUDRead || method == MethodCRUDUpdate || method == MethodCRUDDelete {
			required = append(required, "id")
		}
		if method == MethodCRUDCreate || method == MethodCRUDUpdate {
			required = append(required, "data")
		}
	case MethodMCPCallTool:
		required = []string{"tool_name", "arguments"}
		add(payloadString(true), "tool_name")
		add(payloadObject, "arguments")
		add(payloadString(false), "session_id")
		add(payloadJSON, "identity")
	case MethodHTTPHandle:
		required = []string{"method", "path"}
		add(payloadString(true), "method", "path")
		add(payloadString(false), "raw_path", "raw_query", "session_id")
		add(payloadStrings, "query", "headers")
		add(payloadBase64, "body")
		add(payloadJSON, "identity")
	case MethodGrantsRenew:
		required = []string{"renewal_version", "sequence", "incarnation", "grants", "context"}
		add(payloadJSON, "renewal_version", "sequence", "incarnation", "grants")
	case MethodMigrate:
		required = []string{"from_version", "to_version", "data_dir"}
		add(payloadString(true), required...)
	default:
		return nil, nil
	}
	b, err := paramsJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("unserializable params")
	}
	if err := payloadFiniteNumbers(b); err != nil {
		return nil, fmt.Errorf("decode params: nonfinite JSON number")
	}
	f, err := payloadFields(b, rules, required)
	if err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	if v, ok := f["context"]; ok {
		var c ForwardContext
		if err := json.Unmarshal(v, &c); err != nil {
			return nil, err
		}
		return &c, nil
	}
	return nil, nil
}

func payloadArray(rule payloadRule) payloadRule {
	return func(raw json.RawMessage) error {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || items == nil {
			return fmt.Errorf("expected array")
		}
		for _, v := range items {
			if err := rule(v); err != nil {
				return err
			}
		}
		return nil
	}
}
func payloadClosed(rules map[string]payloadRule, required []string, nullable ...string) payloadRule {
	return func(raw json.RawMessage) error {
		_, err := payloadFields(raw, rules, required, nullable...)
		return err
	}
}
func validateRuntimeResult(result any, raw []byte) error {
	if err := payloadFiniteNumbers(raw); err != nil {
		return err
	}
	str := payloadString(false)
	envelope := payloadClosed(map[string]payloadRule{"type": payloadString(true), "data": payloadObject, "session_id": str}, []string{"type", "data"}, "data")
	envs := payloadArray(envelope)
	var rules map[string]payloadRule
	var required, nullable []string
	switch result.(type) {
	case LoadResult:
		rules = map[string]payloadRule{"skipped_registrations": payloadArray(payloadClosed(map[string]payloadRule{"kind": payloadString(true), "id": payloadString(true), "reason": str}, []string{"kind", "id", "reason"}))}
	case CommandExecResult:
		rules = map[string]payloadRule{"action": str, "content": str, "envelopes": envs}
		required = []string{"action"}
	case EventHandleResult:
		rules = map[string]payloadRule{"cancel": payloadBool, "reason": str, "envelopes": envs}
	case HealthResult:
		rules = map[string]payloadRule{"ok": payloadBool, "message": str}
		required = []string{"ok"}
	case MCPCallResult:
		rules = map[string]payloadRule{"content": payloadJSON, "is_error": payloadBool, "envelopes": envs}
		required = []string{"content"}
		nullable = []string{"content"}
	case HTTPResponse:
		rules = map[string]payloadRule{"status": payloadSafeInteger, "headers": payloadStrings, "body": payloadBase64}
		required = []string{"status"}
	case MigrateResult:
		rules = map[string]payloadRule{"notes": payloadArray(str)}
	case CRUDResult:
		rules = map[string]payloadRule{"data": payloadObject}
		required = []string{"data"}
		nullable = []string{"data"}
	case CRUDListResult:
		rules = map[string]payloadRule{"items": payloadArray(func(v json.RawMessage) error {
			if bytes.Equal(v, []byte("null")) {
				return nil
			}
			return payloadObject(v)
		})}
		required = []string{"items"}
	default:
		return strictjson.Validate(raw)
	}
	_, err := payloadFields(raw, rules, required, nullable...)
	return err
}

// Check source strings before encoding/json can replace invalid UTF-8. Binary
// slices are base64 couriers; raw JSON is checked after serialization.
func payloadResultStrings(v reflect.Value, depth int) error {
	if depth > strictjson.MaxDepth {
		return fmt.Errorf("result nesting exceeds limit")
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return payloadResultStrings(v.Elem(), depth+1)
		}
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return fmt.Errorf("invalid result Unicode")
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if err := payloadResultStrings(it.Key(), depth+1); err != nil {
				return err
			}
			if err := payloadResultStrings(it.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := payloadResultStrings(v.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" {
				if err := payloadResultStrings(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func payloadResultSource(value any) error { return payloadResultStrings(reflect.ValueOf(value), 0) }
func payloadSafeInteger(raw json.RawMessage) error {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < -9007199254740991 || n > 9007199254740991 {
		return fmt.Errorf("invalid safe integer")
	}
	return nil
}

func payloadFiniteNumbers(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	for {
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if n, ok := token.(json.Number); ok {
			v, err := strconv.ParseFloat(string(n), 64)
			if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
				return fmt.Errorf("nonfinite JSON number")
			}
		}
	}
}

// CRUD fields are operation-dependent, but a supplied empty object is present.
// omitempty on a map would erase it and violate create/update's required data.
func (p CRUDParams) MarshalJSON() ([]byte, error) {
	fields := map[string]any{"resource_type": p.ResourceType}
	if p.ID != "" {
		fields["id"] = p.ID
	}
	if p.Data != nil {
		fields["data"] = p.Data
	}
	if p.Filters != nil {
		fields["filters"] = p.Filters
	}
	if p.Context != nil {
		fields["context"] = p.Context
	}
	return json.Marshal(fields)
}

// MarshalJSON emits the required event data object even when the host supplied
// a nil map. The receiver is copied; the caller's map is not changed.
func (p EventHandleParams) MarshalJSON() ([]byte, error) {
	type plain EventHandleParams
	if p.Data == nil {
		p.Data = map[string]interface{}{}
	}
	return json.Marshal(plain(p))
}

// MarshalJSON emits the required tool arguments object for a nil map.
func (p MCPCallRequest) MarshalJSON() ([]byte, error) {
	type plain MCPCallRequest
	if p.Arguments == nil {
		p.Arguments = map[string]interface{}{}
	}
	return json.Marshal(plain(p))
}
