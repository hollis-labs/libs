package capability

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestRequestIDPositiveSafeIntegerRoundTrip(t *testing.T) {
	for _, id := range []RequestID{1, 42, RequestID(MaxSafeInteger)} {
		if err := id.Validate(); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(id)
		if err != nil || string(raw) != fmt.Sprint(uint64(id)) {
			t.Fatalf("numeric encoding: %s %v", raw, err)
		}
		var got RequestID
		if err := json.Unmarshal(raw, &got); err != nil || got != id {
			t.Fatalf("ID roundtrip: %d %v", got, err)
		}
		var padded RequestID
		if err := json.Unmarshal(append(append([]byte(" \n"), raw...), []byte("\t ")...), &padded); err != nil || padded != id {
			t.Fatal("JSON whitespace changed integer")
		}
	}
}

func TestRequestIDRejectsInvalidTokensWithoutChangingReceiver(t *testing.T) {
	for _, raw := range []string{"0", "-1", "1.0", "1.5", "1e0", "1E2", `"1"`, "null", "true", "[]", "{}", "01", "+1", "", "9007199254740992", "18446744073709551615", "18446744073709551616"} {
		t.Run(raw, func(t *testing.T) {
			got := RequestID(42)
			if err := json.Unmarshal([]byte(raw), &got); err == nil || got != 42 {
				t.Fatalf("invalid token accepted or receiver changed: %q %d %v", raw, got, err)
			}
		})
	}
	var nilID *RequestID
	if err := nilID.UnmarshalJSON([]byte("1")); err == nil {
		t.Fatal("nil receiver accepted")
	}
}

func TestRequestIDRejectsInvalidValuesOnValidationAndEncoding(t *testing.T) {
	for _, id := range []RequestID{0, RequestID(MaxSafeInteger + 1), ^RequestID(0)} {
		if id.Validate() == nil {
			t.Fatalf("invalid ID accepted: %d", id)
		}
		if _, err := json.Marshal(id); err == nil {
			t.Fatalf("invalid ID serialized: %d", id)
		}
	}
}

func TestRPCDataRequiresPositiveIDAndPreservesNumericCorrelation(t *testing.T) {
	for _, id := range []RequestID{0, RequestID(MaxSafeInteger + 1)} {
		_, err := (&Error{Code: CapabilityDenied, RequestID: id, EffectState: NotStarted}).RPCData()
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != InvalidRequest {
			t.Fatalf("RPC ID validation missing: %d %v", id, err)
		}
	}
	for _, id := range []RequestID{1, RequestID(MaxSafeInteger)} {
		data, err := (&Error{Code: TargetUnavailable, RequestID: id, EffectState: NotStarted, Detail: ParentInvalid}).RPCData()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["request_id"]) != fmt.Sprint(uint64(id)) || string(fields["retryable"]) != "false" || data.RequestID != id || string(fields["detail"]) != `"parent_invalid"` || data.Detail != ParentInvalid {
			t.Fatalf("wire correlation drift: %s", raw)
		}
	}
	if _, err := (*Error)(nil).RPCData(); err == nil {
		t.Fatal("nil RPC error accepted")
	}
	if _, err := json.Marshal(RPCErrorData{Contract: "host-rpc/1", Code: InternalError, EffectState: NotStarted}); err == nil {
		t.Fatal("payload serialized without correlation")
	}
}

func TestInternalClassificationWithoutRPCID(t *testing.T) {
	e := &Error{Code: CapabilityDenied, EffectState: NotStarted, Detail: ParentTerminal}
	if err := e.Validate(); err != nil {
		t.Fatal("internal classification needs a fabricated RPC ID")
	}
	if _, err := e.RPCData(); err == nil {
		t.Fatal("internal error serialized without request ID")
	}
	e.RequestID = 1
	data, err := e.RPCData()
	if err != nil || data.Detail != ParentTerminal {
		t.Fatal("attaching real correlation lost classification")
	}
	raw, err := json.Marshal(data)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &fields) != nil || string(fields["detail"]) != `"parent_terminal"` {
		t.Fatal("parent terminal wire vocabulary drift")
	}
}

func TestFailureDetailsRemainClosed(t *testing.T) {
	for _, detail := range []FailureDetail{StaleBinding, CallbackCycle, DepthExceeded, ParentInvalid, ParentTerminal} {
		e := &Error{Code: TargetUnavailable, RequestID: 1, EffectState: NotStarted, Detail: detail}
		data, err := e.RPCData()
		if err != nil || data.Detail != detail {
			t.Fatalf("known detail refused: %s %v", detail, err)
		}
	}
	for _, detail := range []FailureDetail{"parent_wrong_binding", "parent_wrong_owner", "Parent_invalid", "parent_invalid ", "raw secret"} {
		e := &Error{Code: TargetUnavailable, RequestID: 1, EffectState: NotStarted, Detail: detail}
		if e.Validate() == nil {
			t.Fatalf("unknown detail accepted: %q", detail)
		}
		if _, err := e.RPCData(); err == nil {
			t.Fatalf("unknown detail emitted: %q", detail)
		}
	}
}
