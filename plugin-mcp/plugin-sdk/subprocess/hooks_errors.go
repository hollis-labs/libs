package subprocess

import "errors"

// HookErrorData is the hooks/1 structural refusal contract. It never signals a
// deliberate veto; those are successful RPC results with structured status.
type HookErrorData struct {
	Contract string  `json:"contract"`
	Code     string  `json:"code"`
	Field    *string `json:"field,omitempty"`
}

// HookValidationError contains safe field metadata, never rejected values.
type HookValidationError struct{ Field, Reason string }

func (e *HookValidationError) Error() string { return "hooks: " + e.Field + ": " + e.Reason }
func hookInvalid(field, reason string) error { return &HookValidationError{field, reason} }

// HookRPCError maps structural/lifecycle errors to standard JSON-RPC codes.
// It does not map engine sentinels, backend text, -32003 or -32010 to a veto.
func HookRPCError(code int, cause string, field string) (*RPCError, error) {
	switch code {
	case ErrCodeParse, ErrCodeInvalidRequest, ErrCodeMethodNotFound, ErrCodeInvalidParams:
	default:
		return nil, hookInvalid("code", "unsupported hooks RPC error code")
	}
	validCause := (code == ErrCodeParse && cause == "parse_error") || (code == ErrCodeInvalidRequest && cause == "invalid_request") || (code == ErrCodeInvalidParams && cause == "invalid_params") || (code == ErrCodeMethodNotFound && (cause == "profile_unavailable" || cause == "method_not_found"))
	if !validCause {
		return nil, hookInvalid("code", "RPC code/cause mismatch")
	}
	data := HookErrorData{Contract: "hooks/1", Code: cause}
	if field != "" {
		data.Field = &field
	}
	if err := data.Validate(); err != nil {
		return nil, err
	}
	return &RPCError{Code: code, Message: "hook request rejected", Data: data}, nil
}
func (s *server) writeHookError(id RPCID, code int, cause string, err error) {
	field := ""
	var failure *HookValidationError
	if errors.As(err, &failure) {
		field = failure.Field
	}
	rpc, _ := HookRPCError(code, cause, field)
	if id == (RPCID{}) {
		s.logger.Warn("hook notification rejected", "code", cause, "field", field)
		return
	}
	s.writeMessage(RPCResponse{JSONRPC: "2.0", ID: id, Error: rpc})
}
