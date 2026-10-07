package subprocess

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hollis-labs/plugin-sdk/capability"
)

func TestHostRPCErrorLeafVocabulary(t *testing.T) {
	for _, detail := range []capability.FailureDetail{capability.StaleBinding, capability.CallbackCycle, capability.DepthExceeded, capability.ParentInvalid, capability.ParentTerminal} {
		leaf, err := (&capability.Error{Code: capability.TargetUnavailable, RequestID: 1, EffectState: capability.NotStarted, Detail: detail}).RPCData()
		if err != nil {
			t.Fatal(err)
		}
		response := ApplicationErrorResponse{JSONRPC: "2.0", ID: 1, Error: HostRPCError{Code: capability.HostRPCErrorCode, Message: "Parent unavailable", Data: HostRPCErrorData(leaf)}}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ApplicationErrorResponse
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Error.Data.Detail != detail || decoded.Error.Data.RequestID != response.ID {
			t.Fatal("typed classification/correlation lost")
		}
	}
}

func TestHostRPCErrorStrictMetadata(t *testing.T) {
	base := `{"contract":"host-rpc/1","code":"target_unavailable","request_id":1,"effect_state":"not_started","retryable":false,"detail":"parent_invalid"}`
	for _, replacement := range []struct{ before, after string }{
		{`"request_id":1`, `"request_id":null`}, {`"request_id":1`, `"request_id":0`},
		{`"request_id":1`, `"request_id":1e0`}, {`"request_id":1`, `"request_id":1.0`},
		{`"request_id":1`, `"request_id":9007199254740992`},
		{`"request_id":1`, `"Request_id":1`}, {`"request_id":1`, `"request_id":1,"request_id":1`},
		{`"retryable":false`, `"retryable":true`}, {`"retryable":false`, `"retryable":null`},
		{`"parent_invalid"`, `"parent_unknown"`}, {`"parent_invalid"`, `""`},
		{`"target_unavailable"`, `"unknown_code"`}, {`"not_started"`, `"unknown_state"`},
		{`"target_unavailable"`, `"unknown_outcome"`},
		{`"contract":"host-rpc/1"`, `"contract":"host-rpc/2"`},
	} {
		raw := strings.Replace(base, replacement.before, replacement.after, 1)
		if err := ValidateHostRPCDTO("HostRPCErrorData", []byte(raw)); err == nil {
			t.Errorf("accepted metadata mutation %s", replacement.after)
		}
	}
	unknown := strings.Replace(strings.Replace(base, `"target_unavailable"`, `"unknown_outcome"`, 1), `"not_started"`, `"unknown"`, 1)
	if err := ValidateHostRPCDTO("HostRPCErrorData", []byte(unknown)); err != nil {
		t.Fatal(err)
	}
	// A valid data DTO still requires equality with its enclosing response ID.
	data := HostRPCErrorData{Contract: "host-rpc/1", Code: capability.TargetUnavailable, RequestID: 1, EffectState: capability.NotStarted}
	response := ApplicationErrorResponse{JSONRPC: "2.0", ID: 2, Error: HostRPCError{Code: capability.HostRPCErrorCode, Message: "safe", Data: data}}
	if _, err := json.Marshal(response); err == nil {
		t.Fatal("accepted mismatched correlation")
	}
	for _, message := range []string{strings.Repeat("😀", 257), "\xff"} {
		response.ID = 1
		response.Error.Message = message
		if _, err := json.Marshal(response); err == nil {
			t.Fatal("accepted invalid/oversized message")
		}
	}
	response.Error.Message = strings.Repeat("😀", 256)
	if _, err := json.Marshal(response); err != nil {
		t.Fatal(err)
	}
}
