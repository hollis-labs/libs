package subprocess

import (
	"encoding/json"
	"errors"
	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
)

type CancelReason string

const (
	CallerCancelled   CancelReason = "caller_cancelled"
	DeadlineExpired   CancelReason = "deadline_exceeded"
	ParentCancelled   CancelReason = "parent_cancelled"
	ConnectionClosing CancelReason = "connection_closing"
)

type CancelParams struct {
	RequestOwner HostRPCRequestOwner `json:"request_owner"`
	ID           RPCID               `json:"id"`
	Reason       CancelReason        `json:"reason"`
}

// PluginRPCErrorData is a closed base-domain transport failure. The enclosing
// RPCID supplies correlation; no positive request_id is fabricated.
type PluginRPCErrorData struct {
	Contract    string                 `json:"contract"`
	Code        capability.Code        `json:"code"`
	EffectState capability.EffectState `json:"effect_state"`
	Retryable   bool                   `json:"retryable"`
}

func ValidateRPCControlDTO(name string, raw []byte, directional bool) error {
	if len(raw) > DefaultFrameBytes || strictjson.ValidatePortable(raw) != nil {
		return errors.New("subprocess: invalid control DTO")
	}
	var rule hostRPCRule
	switch name {
	case "CancelParams":
		rule = hostRPCObject(map[string]hostRPCRule{"request_owner": hostRPCEnum(string(HostRPCOwnerHost), string(HostRPCOwnerPlugin)), "reason": hostRPCEnum(string(CallerCancelled), string(DeadlineExpired), string(ParentCancelled), string(ConnectionClosing)), "id": func(raw json.RawMessage, field string) error {
			id, err := parseRPCID(raw)
			if err != nil || id == (RPCID{}) || (directional && !id.positiveInteger()) {
				return hostRPCInvalid(field, "invalid cancellation ID")
			}
			return nil
		}})
	case "PluginRPCErrorData":
		rule = hostRPCObject(map[string]hostRPCRule{"contract": hostRPCEnum("plugin-rpc/2"), "code": hostRPCEnum(string(capability.RateLimited), string(capability.Cancelled), string(capability.DeadlineExceeded), string(capability.BudgetExceeded), string(capability.UnknownOutcome)), "effect_state": hostRPCEnum(string(capability.NotStarted), string(capability.NotCommitted), string(capability.Committed), string(capability.Unknown)), "retryable": hostRPCFalse})
	default:
		return errors.New("subprocess: unknown control DTO")
	}
	if err := rule(raw, name); err != nil {
		return err
	}
	if name == "PluginRPCErrorData" {
		type plain PluginRPCErrorData
		var v plain
		if json.Unmarshal(raw, &v) != nil {
			return errors.New("subprocess: invalid error DTO")
		}
		if v.Code == capability.RateLimited && v.EffectState != capability.NotStarted {
			return errors.New("subprocess: invalid admission effect state")
		}
		// The capability leaf owns unknown_outcome's relation to effect state.
		if (&capability.Error{Code: v.Code, EffectState: v.EffectState}).Validate() != nil {
			return errors.New("subprocess: invalid failure classification")
		}
	}
	return nil
}
func (v CancelParams) MarshalJSON() ([]byte, error) {
	type plain CancelParams
	b, err := marshalBounded(plain(v), DefaultFrameBytes)
	if err == nil {
		err = ValidateRPCControlDTO("CancelParams", b, false)
	}
	return b, err
}
func (v *CancelParams) UnmarshalJSON(b []byte) error {
	if err := ValidateRPCControlDTO("CancelParams", b, false); err != nil {
		return err
	}
	type plain CancelParams
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return err
	}
	*v = CancelParams(next)
	return nil
}
func (v PluginRPCErrorData) MarshalJSON() ([]byte, error) {
	type plain PluginRPCErrorData
	b, err := marshalBounded(plain(v), TerminalCreditBytes)
	if err == nil {
		err = ValidateRPCControlDTO("PluginRPCErrorData", b, false)
	}
	return b, err
}
func (v *PluginRPCErrorData) UnmarshalJSON(b []byte) error {
	if err := ValidateRPCControlDTO("PluginRPCErrorData", b, false); err != nil {
		return err
	}
	type plain PluginRPCErrorData
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return err
	}
	*v = PluginRPCErrorData(next)
	return nil
}
func controlErrorData(id RPCID, code capability.Code, state capability.EffectState) json.RawMessage {
	var value any
	if n, ok := id.Integer(); ok && n > 0 {
		value = HostRPCErrorData{Contract: "host-rpc/1", Code: code, RequestID: capability.RequestID(n), EffectState: state, Retryable: false}
	} else {
		value = PluginRPCErrorData{"plugin-rpc/2", code, state, false}
	}
	raw, _ := json.Marshal(value)
	return raw
}
func requestFailureResponse(id RPCID, code capability.Code, state capability.EffectState) RPCResponse {
	number := ErrCodeInternal
	if id.positiveInteger() {
		number = capability.HostRPCErrorCode
	}
	return RPCResponse{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: number, Message: string(code), Data: controlErrorData(id, code, state)}}
}

// RPC transport cancellation is distinct from the hook-veto sentinel.
type TransportCancelledError struct{ Reason CancelReason }

func (e *TransportCancelledError) Error() string {
	return "RPC transport cancelled: " + string(e.Reason)
}

type DeadlineExceededError struct{}

func (*DeadlineExceededError) Error() string { return "RPC deadline exceeded" }

// RPCTransportError retains safe classification separately from the local cause.
// Helpers serialize Failure only; a transport never copies Cause into wire data.
type RPCTransportError struct {
	Failure capability.Error
	cause   error
}

func (e *RPCTransportError) Error() string { return "RPC transport: " + string(e.Failure.Code) }
func (e *RPCTransportError) Unwrap() error { return e.cause }
