package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
)

const maxRPCInteger int64 = 9007199254740991

// RPCID is a string or a JavaScript-safe integer. Its zero value denotes an
// absent request ID, or the null ID of an error response. NumberID(0) is a
// request ID, not a notification. Decoding never rounds numeric IDs.
type RPCID struct {
	kind   byte
	number int64
	text   string
}

// NumberID constructs an integer ID; encoding rejects values outside the safe range.
func NumberID(n int64) RPCID { return RPCID{kind: 'n', number: n} }

// StringID constructs a string ID, including an empty string.
func StringID(s string) RPCID { return RPCID{kind: 's', text: s} }

// Integer returns the integer value and whether this ID is numeric.
func (id RPCID) Integer() (int64, bool) { return id.number, id.kind == 'n' }

// Text returns the string value and whether this ID is a string.
func (id RPCID) Text() (string, bool) { return id.text, id.kind == 's' }
func (id RPCID) positiveInteger() bool {
	return id.kind == 'n' && id.number > 0 && id.number <= maxRPCInteger
}
func (id RPCID) MarshalJSON() ([]byte, error) {
	switch id.kind {
	case 0:
		return []byte("null"), nil
	case 's':
		return json.Marshal(id.text)
	case 'n':
		if id.number < -maxRPCInteger || id.number > maxRPCInteger {
			return nil, fmt.Errorf("unsafe RPC integer ID")
		}
		return []byte(strconv.FormatInt(id.number, 10)), nil
	default:
		return nil, fmt.Errorf("invalid RPC ID")
	}
}
func (id *RPCID) UnmarshalJSON(data []byte) error {
	next, err := parseRPCID(data)
	if err != nil {
		return err
	}
	*id = next
	return nil
}
func parseRPCID(data []byte) (RPCID, error) {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return RPCID{}, nil
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := strictjson.Validate(data); err != nil {
			return RPCID{}, err
		}
		if err := json.Unmarshal(data, &s); err != nil {
			return RPCID{}, err
		}
		return StringID(s), nil
	}
	// ParseInt rejects decimal/exponent tokens; JSON validity rejects leading zeros.
	if !json.Valid(data) {
		return RPCID{}, fmt.Errorf("invalid RPC ID")
	}
	n, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil || n < -maxRPCInteger || n > maxRPCInteger {
		return RPCID{}, fmt.Errorf("invalid RPC integer ID")
	}
	return NumberID(n), nil
}

// MarshalJSON omits only an absent ID; numeric zero and empty strings survive.
func (r RPCRequest) MarshalJSON() ([]byte, error) {
	type wire struct {
		JSONRPC string `json:"jsonrpc"`
		ID      *RPCID `json:"id,omitempty"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}
	var id *RPCID
	if r.ID != (RPCID{}) {
		id = &r.ID
	}
	return json.Marshal(wire{r.JSONRPC, id, r.Method, r.Params})
}
func (r *RPCRequest) UnmarshalJSON(data []byte) error {
	next, fault := decodeEnvelope(data)
	if fault != nil {
		return fault.Error
	}
	if next == nil {
		return fmt.Errorf("expected RPC request, received response")
	}
	*r = *next
	return nil
}

// decodeEnvelope separates JSON syntax, envelope structure and method payloads.
// A nil request and nil fault identifies a valid response. The correlation
// engine consumes its raw fields separately. Payload validation remains method-owned.
func decodeEnvelope(data []byte) (*RPCRequest, *RPCResponse) {
	fail := func(id RPCID, code int, message string) (*RPCRequest, *RPCResponse) {
		return nil, &RPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: message}}
	}
	if !json.Valid(data) {
		return fail(RPCID{}, ErrCodeParse, "parse error: invalid JSON")
	}
	if b := bytes.TrimSpace(data); len(b) == 0 || b[0] != '{' {
		return fail(RPCID{}, ErrCodeInvalidRequest, "invalid request")
	}
	// Decode each top-level value as raw JSON, so duplicate envelope keys and
	// numeric token spellings survive. Nested payloads are validated by their DTO.
	d := json.NewDecoder(bytes.NewReader(data))
	_, _ = d.Token()
	fields := make(map[string]json.RawMessage)
	duplicate := false
	duplicateID := false
	invalidKeys := false
	for d.More() {
		start := d.InputOffset()
		token, _ := d.Token()
		if strictjson.Validate(bytes.TrimLeft(data[start:d.InputOffset()], ", \t\r\n")) != nil {
			invalidKeys = true
		}
		key := token.(string)
		if _, exists := fields[key]; exists {
			duplicate = true
			duplicateID = duplicateID || key == "id"
		}
		var raw json.RawMessage
		_ = d.Decode(&raw) // json.Valid established syntax above.
		fields[key] = raw
	}
	var id RPCID
	rawID, hasID := fields["id"]
	if hasID && !duplicateID {
		var err error
		id, err = parseRPCID(rawID)
		if err != nil {
			return fail(RPCID{}, ErrCodeInvalidRequest, "invalid request")
		}
	}
	if duplicateID {
		id = RPCID{}
	}
	var version string
	if invalidKeys || duplicate || json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" {
		return fail(id, ErrCodeInvalidRequest, "invalid request")
	}
	_, hasMethod := fields["method"]
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	_, hasParams := fields["params"]
	if !hasMethod && hasID && hasResult != hasError {
		// Null IDs are valid only for errors. Error objects need code/message.
		if hasParams || (hasResult && id == (RPCID{})) {
			return fail(id, ErrCodeInvalidRequest, "invalid request")
		}
		if hasError {
			var errorFields map[string]json.RawMessage
			if json.Unmarshal(fields["error"], &errorFields) != nil {
				return fail(id, ErrCodeInvalidRequest, "invalid request")
			}
			var code *float64
			var message *string
			if json.Unmarshal(errorFields["code"], &code) != nil || code == nil || math.Trunc(*code) != *code || math.Abs(*code) > float64(maxRPCInteger) || json.Unmarshal(errorFields["message"], &message) != nil || message == nil || strictjson.Validate(errorFields["message"]) != nil {
				return fail(id, ErrCodeInvalidRequest, "invalid request")
			}
		}
		return nil, nil
	}
	if hasResult || hasError || !hasMethod || (hasID && id == (RPCID{})) {
		return fail(id, ErrCodeInvalidRequest, "invalid request")
	}
	var method string
	if len(fields["method"]) == 0 || fields["method"][0] != '"' || json.Unmarshal(fields["method"], &method) != nil || strictjson.Validate(fields["method"]) != nil {
		return fail(id, ErrCodeInvalidRequest, "invalid request")
	}
	var params any
	if raw, ok := fields["params"]; ok {
		params = raw
	}
	return &RPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}, nil
}

// Exact decoded top-level keys classify faults without examining business data.
func replyCandidate(raw []byte) bool {
	if !json.Valid(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	for d.More() {
		key, _ := d.Token()
		var value json.RawMessage
		_ = d.Decode(&value)
		if key == "result" || key == "error" {
			return true
		}
	}
	return false
}
