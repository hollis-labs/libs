package subprocess

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/internal/strictjson"
)

// MaxHostRPCDTOBytes caps each encoded params/result DTO, including base64 text.
const MaxHostRPCDTOBytes = 1 << 20

// HostRPCValidationError contains safe field metadata, never rejected values.
// It is a structural failure; RPC adapters decide whether to emit -32602.
type HostRPCValidationError struct{ Field, Reason string }

func (e *HostRPCValidationError) Error() string { return "host RPC: " + e.Field + ": " + e.Reason }
func hostRPCInvalid(field, reason string) error { return &HostRPCValidationError{field, reason} }

type hostRPCRule func(json.RawMessage, string) error

var hostRPCTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)
var hostRPCHeaderName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// Match ECMAScript/JSON Schema whitespace, rather than Go's broader TrimSpace.
func hostRPCSpace(r rune) bool {
	return r >= 0x09 && r <= 0x0d || r == 0x20 || r == 0xa0 || r == 0x1680 ||
		r >= 0x2000 && r <= 0x200a || r == 0x2028 || r == 0x2029 ||
		r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
}
func hostRPCString(max int, nonblank bool) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		var s *string
		if json.Unmarshal(raw, &s) != nil || s == nil || utf8.RuneCountInString(*s) > max ||
			nonblank && strings.TrimFunc(*s, hostRPCSpace) == "" {
			return hostRPCInvalid(field, "invalid bounded string")
		}
		return nil
	}
}
func hostRPCInteger(min, max uint64) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		b := bytes.TrimSpace(raw)
		if len(b) == 0 {
			return hostRPCInvalid(field, "required decimal integer")
		}
		for _, c := range b {
			if c < '0' || c > '9' {
				return hostRPCInvalid(field, "required decimal integer")
			}
		}
		n, err := strconv.ParseUint(string(b), 10, 64)
		if err != nil || n < min || n > max {
			return hostRPCInvalid(field, "integer outside range")
		}
		return nil
	}
}
func hostRPCEnum(values ...string) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		var s *string
		if json.Unmarshal(raw, &s) == nil && s != nil {
			for _, v := range values {
				if *s == v {
					return nil
				}
			}
		}
		return hostRPCInvalid(field, "unknown enum value")
	}
}
func hostRPCBool(raw json.RawMessage, field string) error {
	var v *bool
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return hostRPCInvalid(field, "required boolean")
	}
	return nil
}
func hostRPCTrue(raw json.RawMessage, field string) error {
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
		return hostRPCInvalid(field, "required true")
	}
	return nil
}
func hostRPCOpaque(raw json.RawMessage, field string) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || strictjson.ValidatePortable(raw) != nil {
		return hostRPCInvalid(field, "required portable non-null JSON")
	}
	return nil
}
func hostRPCNullable(rule hostRPCRule) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return rule(raw, field)
	}
}
func hostRPCRef(name string) hostRPCRule {
	return func(raw json.RawMessage, field string) error { return hostRPCShapes[name](raw, field) }
}
func hostRPCArray(rule hostRPCRule, max int) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || items == nil || len(items) > max {
			return hostRPCInvalid(field, "required bounded array")
		}
		for _, item := range items {
			if err := rule(item, field); err != nil {
				return err
			}
		}
		return nil
	}
}
func hostRPCObject(rules map[string]hostRPCRule, optional ...string) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return hostRPCInvalid(field, "required closed object")
		}
		opts := make(map[string]bool, len(optional))
		for _, k := range optional {
			opts[k] = true
		}
		for k := range values {
			if _, ok := rules[k]; !ok {
				return hostRPCInvalid(field, "unknown or incorrectly cased field")
			}
		}
		for k, rule := range rules {
			v, present := values[k]
			if !present {
				if opts[k] {
					continue
				}
				return hostRPCInvalid(field+"."+k, "required field")
			}
			if err := rule(v, field+"."+k); err != nil {
				return err
			}
		}
		return nil
	}
}
func hostRPCTime(raw json.RawMessage, field string) error {
	var s string
	if json.Unmarshal(raw, &s) != nil || !hostRPCTimestamp.MatchString(s) {
		return hostRPCInvalid(field, "invalid UTC timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
		return hostRPCInvalid(field, "invalid UTC timestamp")
	}
	return nil
}
func hostRPCBase64(raw json.RawMessage, field string) error {
	var s *string
	if json.Unmarshal(raw, &s) != nil || s == nil {
		return hostRPCInvalid(field, "required base64 string")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(*s)
	if err != nil || base64.StdEncoding.EncodeToString(b) != *s {
		return hostRPCInvalid(field, "noncanonical padded base64")
	}
	return nil
}
func hostRPCHeader(raw json.RawMessage, field string) error {
	if err := hostRPCObject(map[string]hostRPCRule{"name": hostRPCString(256, true), "value": hostRPCString(8192, false)})(raw, field); err != nil {
		return err
	}
	var h struct{ Name, Value string }
	_ = json.Unmarshal(raw, &h)
	if !hostRPCHeaderName.MatchString(h.Name) || strings.ContainsAny(h.Value, "\r\n\x00") {
		return hostRPCInvalid(field, "invalid HTTP header")
	}
	return nil
}
func hostRPCURL(raw json.RawMessage, field string) error {
	if err := hostRPCString(8192, true)(raw, field); err != nil {
		return err
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	// The adapter additionally owns DNS/redirect/proxy/destination policy.
	u, err := parseHostRPCURL(s)
	if err != nil || !u {
		return hostRPCInvalid(field, "required absolute HTTPS URL without credentials or fragment")
	}
	return nil
}
func hostRPCLogFields(raw json.RawMessage, field string) error {
	if err := hostRPCArray(hostRPCRef("LogField"), 128)(raw, field); err != nil {
		return err
	}
	var fields []struct{ Name string }
	_ = json.Unmarshal(raw, &fields)
	seen := map[string]bool{}
	for _, f := range fields {
		if seen[f.Name] {
			return hostRPCInvalid(field, "duplicate log field name")
		}
		seen[f.Name] = true
	}
	return nil
}

// ValidateHostRPCDTO accepts only the closed structural DTO vocabulary. It
// validates JSON, not authority, service dispatch, or transport correlation.
// Host-owned opaque query/tool schemas must be checked by their owners.
func ValidateHostRPCDTO(name string, data []byte) error {
	rule, ok := hostRPCShapes[name]
	if !ok {
		return hostRPCInvalid("dto", "unsupported DTO")
	}
	if len(data) > MaxHostRPCDTOBytes {
		return hostRPCInvalid(name, "encoded DTO exceeds byte limit")
	}
	if strictjson.ValidatePortable(data) != nil {
		return hostRPCInvalid(name, "invalid or ambiguous portable JSON")
	}
	return rule(data, name)
}

// Preflight bounds authored values before encoding can make an escaped copy.
// Final validation counts the actual encoded bytes including keys and escaping.
func hostRPCPreflight(v reflect.Value, depth int, bytesLeft *int) error {
	if depth > strictjson.MaxDepth {
		return hostRPCInvalid("dto", "JSON nesting exceeds limit")
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return hostRPCPreflight(v.Elem(), depth+1, bytesLeft)
		}
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return hostRPCInvalid("dto", "invalid UTF-8 string")
		}
		*bytesLeft -= v.Len()
	case reflect.Slice:
		if v.Type() == reflect.TypeOf(json.RawMessage{}) {
			*bytesLeft -= v.Len()
			if *bytesLeft < 0 {
				return hostRPCInvalid("dto", "DTO exceeds byte limit")
			}
			if v.Len() > 0 && strictjson.ValidatePortable(v.Bytes()) != nil {
				return hostRPCInvalid("dto", "invalid portable raw JSON")
			}
		} else {
			if v.Len() > MaxHostRPCDTOBytes {
				return hostRPCInvalid("dto", "array exceeds byte limit")
			}
			for i := 0; i < v.Len(); i++ {
				if err := hostRPCPreflight(v.Index(i), depth+1, bytesLeft); err != nil {
					return err
				}
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := hostRPCPreflight(v.Field(i), depth+1, bytesLeft); err != nil {
				return err
			}
		}
	}
	if *bytesLeft < 0 {
		return hostRPCInvalid("dto", "DTO exceeds byte limit")
	}
	return nil
}
func hostRPCMarshal(name string, value any) ([]byte, error) {
	remaining := MaxHostRPCDTOBytes
	if err := hostRPCPreflight(reflect.ValueOf(value), 0, &remaining); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, hostRPCInvalid(name, "unserializable DTO")
	}
	if err := ValidateHostRPCDTO(name, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

var hostRPCShapes map[string]hostRPCRule

func init() {
	hostRPCShapes = map[string]hostRPCRule{
		"Token":               hostRPCString(4096, true),
		"SafePositiveInteger": hostRPCInteger(1, capability.MaxSafeInteger),
		"TimeoutMs":           hostRPCInteger(1, 4294967295),
		"SchemaVersion":       hostRPCInteger(1, 4294967295),
		"OpaqueJSON":          hostRPCOpaque,
		"Timestamp":           hostRPCTime,
		"Base64":              hostRPCBase64,
		"Headers":             hostRPCArray(hostRPCHeader, 128),
		"LogFields":           hostRPCLogFields,
		"MCPContent":          hostRPCArray(hostRPCOpaque, 1024),
		"ParentCall":          hostRPCObject(map[string]hostRPCRule{"request_owner": hostRPCEnum("host", "plugin"), "id": hostRPCRef("SafePositiveInteger")}),
		"ForwardContext":      hostRPCObject(map[string]hostRPCRule{"binding_id": hostRPCRef("Token"), "timeout_ms": hostRPCRef("TimeoutMs")}, "binding_id"),
		"ReverseContext":      hostRPCObject(map[string]hostRPCRule{"binding_id": hostRPCRef("Token"), "timeout_ms": hostRPCRef("TimeoutMs"), "parent_call": hostRPCRef("ParentCall")}),
		"Header":              hostRPCHeader,
		"LogField":            hostRPCObject(map[string]hostRPCRule{"name": hostRPCString(256, true), "value": hostRPCRef("OpaqueJSON")}),
		"RemainingBudgets":    hostRPCObject(map[string]hostRPCRule{"timeout_ms": hostRPCRef("TimeoutMs"), "bytes": hostRPCInteger(0, 9007199254740991), "effects": hostRPCInteger(0, 9007199254740991), "tokens": hostRPCInteger(0, 9007199254740991)}, "bytes", "effects", "tokens"),
		"StorageGetParams":    hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "key": hostRPCString(4096, true)}),
		"StorageGetResult":    hostRPCObject(map[string]hostRPCRule{"found": hostRPCBool, "value": hostRPCRef("OpaqueJSON"), "revision": hostRPCRef("Token")}, "value", "revision"),
		"StoragePutParams":    hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "key": hostRPCString(4096, true), "value": hostRPCRef("OpaqueJSON"), "expected_revision": hostRPCNullable(hostRPCRef("Token")), "operation_key": hostRPCRef("Token")}),
		"StoragePutResult":    hostRPCObject(map[string]hostRPCRule{"operation_key": hostRPCRef("Token"), "revision": hostRPCRef("Token")}),
		"StorageDeleteParams": hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "key": hostRPCString(4096, true), "expected_revision": hostRPCRef("Token"), "operation_key": hostRPCRef("Token")}),
		"StorageDeleteResult": hostRPCObject(map[string]hostRPCRule{"operation_key": hostRPCRef("Token"), "deleted": hostRPCTrue, "revision": hostRPCRef("Token")}),
		"SecretsGetParams":    hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "secret_ref": hostRPCRef("Token")}),
		"SecretsGetResult":    hostRPCObject(map[string]hostRPCRule{"value_base64": hostRPCRef("Base64"), "expires_at": hostRPCRef("Timestamp")}),
		"EgressRequestParams": hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "method": hostRPCEnum("GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"), "url": hostRPCURL, "headers": hostRPCRef("Headers"), "body_base64": hostRPCRef("Base64"), "operation_key": hostRPCRef("Token")}, "headers", "body_base64", "operation_key"),
		"EgressRequestResult": hostRPCObject(map[string]hostRPCRule{"status": hostRPCInteger(100, 599), "headers": hostRPCRef("Headers"), "body_base64": hostRPCRef("Base64"), "operation_key": hostRPCRef("Token")}, "operation_key"),
		"EventsPublishParams": hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "event_name": hostRPCString(256, true), "payload": hostRPCRef("OpaqueJSON"), "operation_key": hostRPCRef("Token")}),
		"EventsPublishResult": hostRPCObject(map[string]hostRPCRule{"operation_key": hostRPCRef("Token"), "accepted": hostRPCTrue, "event_id": hostRPCRef("Token")}),
		"LogParams":           hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "level": hostRPCEnum("debug", "info", "warn", "error"), "message": hostRPCString(16384, false), "fields": hostRPCRef("LogFields")}, "fields"),
		"LogResult":           hostRPCObject(map[string]hostRPCRule{"accepted": hostRPCBool}),
		"ReadonlyQueryParams": hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "resource": hostRPCString(256, true), "schema_version": hostRPCRef("SchemaVersion"), "params": hostRPCRef("OpaqueJSON")}),
		"ReadonlyQueryResult": hostRPCObject(map[string]hostRPCRule{"resource": hostRPCString(256, true), "schema_version": hostRPCRef("SchemaVersion"), "data": hostRPCRef("OpaqueJSON")}),
		"MCPListToolsParams":  hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "server_id": hostRPCRef("Token"), "cursor": hostRPCRef("Token")}, "cursor"),
		"MCPListToolsResult":  hostRPCObject(map[string]hostRPCRule{"server_id": hostRPCRef("Token"), "tools": hostRPCArray(hostRPCRef("MCPTool"), 256), "next_cursor": hostRPCRef("Token")}, "next_cursor"),
		"MCPCallToolParams":   hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "server_id": hostRPCRef("Token"), "tool_name": hostRPCRef("Token"), "tool_binding": hostRPCRef("Token"), "arguments": hostRPCRef("OpaqueJSON"), "operation_key": hostRPCRef("Token")}, "operation_key"),
		"MCPCallToolResult":   hostRPCObject(map[string]hostRPCRule{"content": hostRPCRef("MCPContent"), "is_error": hostRPCBool, "structured_content": hostRPCRef("OpaqueJSON"), "operation_key": hostRPCRef("Token")}, "structured_content", "operation_key"),
		"MCPCancelCallParams": hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "target_call_id": hostRPCRef("SafePositiveInteger")}),
		"MCPCancelCallResult": hostRPCObject(map[string]hostRPCRule{"accepted": hostRPCBool, "already_terminal": hostRPCBool}),
		"BindingsRenewParams": hostRPCObject(map[string]hostRPCRule{"grant_id": hostRPCRef("Token"), "context": hostRPCRef("ReverseContext"), "requested_lease_ms": hostRPCInteger(1, 300000)}),
		"BindingsRenewResult": hostRPCObject(map[string]hostRPCRule{"binding_id": hostRPCRef("Token"), "expires_at": hostRPCRef("Timestamp"), "remaining_budgets": hostRPCRef("RemainingBudgets")}),
		"MCPTool":             hostRPCObject(map[string]hostRPCRule{"tool_name": hostRPCRef("Token"), "description": hostRPCString(16384, false), "input_schema": hostRPCRef("OpaqueJSON"), "output_schema": hostRPCRef("OpaqueJSON"), "effect": hostRPCEnum("read", "write", "destructive"), "tool_binding": hostRPCRef("Token")}, "description", "output_schema"),
	}
	for name, rule := range hostRPCErrorRules() {
		hostRPCShapes[name] = rule
	}
	hostRPCShapes["StorageGetResult"] = hostRPCGetResult(hostRPCShapes["StorageGetResult"])
	hostRPCShapes["EgressRequestParams"] = hostRPCEgressParams(hostRPCShapes["EgressRequestParams"])
	hostRPCShapes["MCPCancelCallResult"] = hostRPCCancelResult(hostRPCShapes["MCPCancelCallResult"])
}
func hostRPCGetResult(base hostRPCRule) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		if err := base(raw, field); err != nil {
			return err
		}
		var v map[string]json.RawMessage
		_ = json.Unmarshal(raw, &v)
		_, value := v["value"]
		_, revision := v["revision"]
		found := bytes.Equal(bytes.TrimSpace(v["found"]), []byte("true"))
		if found && (!value || !revision) || !found && (value || revision) {
			return hostRPCInvalid(field, "found/value/revision mismatch")
		}
		return nil
	}
}
func hostRPCEgressParams(base hostRPCRule) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		if err := base(raw, field); err != nil {
			return err
		}
		var v map[string]json.RawMessage
		_ = json.Unmarshal(raw, &v)
		var method string
		_ = json.Unmarshal(v["method"], &method)
		switch method {
		case "POST", "PUT", "PATCH", "DELETE":
			if _, ok := v["operation_key"]; !ok {
				return hostRPCInvalid(field+".operation_key", "required mutation key")
			}
		}
		return nil
	}
}
func hostRPCCancelResult(base hostRPCRule) hostRPCRule {
	return func(raw json.RawMessage, field string) error {
		if err := base(raw, field); err != nil {
			return err
		}
		var v struct {
			Accepted        bool
			AlreadyTerminal bool `json:"already_terminal"`
		}
		_ = json.Unmarshal(raw, &v)
		if v.Accepted == v.AlreadyTerminal {
			return hostRPCInvalid(field, "exclusive cancel flags required")
		}
		return nil
	}
}
func parseHostRPCURL(s string) (bool, error) {
	// url.Parse checks path escapes but leaves malformed query escapes intact.
	// Validate the complete wire string, matching the TypeScript URL validator.
	hex := func(b byte) bool {
		return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && (i+2 >= len(s) || !hex(s[i+1]) || !hex(s[i+2])) {
			return false, nil
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return false, err
	}
	if strings.ContainsFunc(s, func(r rune) bool { return r <= 0x20 || r == 0x7f }) {
		return false, nil
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n > 65535 {
			return false, nil
		}
	}
	return strings.HasPrefix(s, "https://") && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && !strings.ContainsAny(s, "#\\"), nil
}
