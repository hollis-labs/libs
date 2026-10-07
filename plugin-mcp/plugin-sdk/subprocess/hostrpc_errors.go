package subprocess

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

// HostRPCErrorData uses the capability leaf's exact field types and vocabulary.
// Its transport codec adds closed-object/raw-token checks before leaf validation.
type HostRPCErrorData capability.RPCErrorData

// HostRPCError is the bounded application failure, distinct from InitError.
// Hosts supply safe messages; the SDK never constructs one from backend errors.
type HostRPCError struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    HostRPCErrorData `json:"data"`
}

func (v HostRPCError) Error() string { return "host RPC: " + string(v.Data.Code) }

// ApplicationErrorResponse retains positive profile correlation separately from
// the broader base JSON-RPC ID domain. Data.RequestID must equal ID.
type ApplicationErrorResponse struct {
	JSONRPC string               `json:"jsonrpc"`
	ID      capability.RequestID `json:"id"`
	Error   HostRPCError         `json:"error"`
}

func hostRPCRequestID(raw json.RawMessage, field string) error {
	var id capability.RequestID
	if json.Unmarshal(raw, &id) != nil {
		return hostRPCInvalid(field, "required positive safe request ID")
	}
	return nil
}
func hostRPCFalse(raw json.RawMessage, field string) error {
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
		return hostRPCInvalid(field, "automatic retry prohibited")
	}
	return nil
}
func hostRPCErrorRules() map[string]hostRPCRule {
	dataShape := hostRPCObject(map[string]hostRPCRule{
		"contract":     hostRPCEnum("host-rpc/1"),
		"code":         hostRPCString(256, true),
		"request_id":   hostRPCRequestID,
		"effect_state": hostRPCString(256, true),
		"retryable":    hostRPCFalse,
		"detail":       hostRPCString(256, true),
	}, "detail")
	data := func(raw json.RawMessage, field string) error {
		if err := dataShape(raw, field); err != nil {
			return err
		}
		var v capability.RPCErrorData
		if json.Unmarshal(raw, &v) != nil {
			return hostRPCInvalid(field, "invalid error metadata")
		}
		// RPCData owns the closed code/detail/state vocabulary and unknown-outcome
		// relationship; transport does not define a second catalog.
		_, err := (&capability.Error{Code: v.Code, RequestID: v.RequestID, EffectState: v.EffectState, Detail: v.Detail}).RPCData()
		if err != nil {
			return hostRPCInvalid(field, "invalid capability error metadata")
		}
		return nil
	}
	errorShape := hostRPCObject(map[string]hostRPCRule{"code": func(raw json.RawMessage, field string) error {
		if !bytes.Equal(bytes.TrimSpace(raw), []byte(strconv.Itoa(capability.HostRPCErrorCode))) {
			return hostRPCInvalid(field, "required application error code")
		}
		return nil
	}, "message": hostRPCString(256, false), "data": hostRPCRef("HostRPCErrorData")})
	responseShape := hostRPCObject(map[string]hostRPCRule{"jsonrpc": hostRPCEnum("2.0"), "id": hostRPCRequestID, "error": hostRPCRef("HostRPCError")})
	response := func(raw json.RawMessage, field string) error {
		if err := responseShape(raw, field); err != nil {
			return err
		}
		type plain ApplicationErrorResponse
		var v plain
		if json.Unmarshal(raw, &v) != nil || v.ID != v.Error.Data.RequestID {
			return hostRPCInvalid(field, "request ID correlation mismatch")
		}
		return nil
	}
	return map[string]hostRPCRule{"HostRPCErrorData": data, "HostRPCError": errorShape, "ApplicationErrorResponse": response}
}

func (v HostRPCErrorData) MarshalJSON() ([]byte, error) {
	type plain HostRPCErrorData
	return hostRPCMarshal("HostRPCErrorData", plain(v))
}
func (v *HostRPCErrorData) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("HostRPCErrorData", data); err != nil {
		return err
	}
	type plain HostRPCErrorData
	var next plain
	if json.Unmarshal(data, &next) != nil {
		return hostRPCInvalid("HostRPCErrorData", "invalid field type")
	}
	*v = HostRPCErrorData(next)
	return nil
}
func (v HostRPCErrorData) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HostRPCError) MarshalJSON() ([]byte, error) {
	type plain HostRPCError
	return hostRPCMarshal("HostRPCError", plain(v))
}
func (v *HostRPCError) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("HostRPCError", data); err != nil {
		return err
	}
	type plain HostRPCError
	var next plain
	if json.Unmarshal(data, &next) != nil {
		return hostRPCInvalid("HostRPCError", "invalid field type")
	}
	*v = HostRPCError(next)
	return nil
}
func (v HostRPCError) Validate() error { _, err := v.MarshalJSON(); return err }

func (v ApplicationErrorResponse) MarshalJSON() ([]byte, error) {
	type plain ApplicationErrorResponse
	return hostRPCMarshal("ApplicationErrorResponse", plain(v))
}
func (v *ApplicationErrorResponse) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("ApplicationErrorResponse", data); err != nil {
		return err
	}
	type plain ApplicationErrorResponse
	var next plain
	if json.Unmarshal(data, &next) != nil {
		return hostRPCInvalid("ApplicationErrorResponse", "invalid field type")
	}
	*v = ApplicationErrorResponse(next)
	return nil
}
func (v ApplicationErrorResponse) Validate() error { _, err := v.MarshalJSON(); return err }
