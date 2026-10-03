package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeIDs(t *testing.T) {
	for _, id := range []RPCID{NumberID(0), NumberID(-1), NumberID(maxRPCInteger), NumberID(-maxRPCInteger), StringID(""), StringID("quote\"\\\n😀")} {
		t.Run(stringIDForTest(id), func(t *testing.T) {
			data, err := json.Marshal(RPCRequest{JSONRPC: "2.0", ID: id, Method: MethodHealth})
			if err != nil {
				t.Fatal(err)
			}
			req, fault := decodeEnvelope(data)
			if fault != nil || req == nil || req.ID != id {
				t.Fatalf("round trip: req=%+v fault=%+v", req, fault)
			}
			var resp RPCResponse
			data, err = json.Marshal(RPCResponse{JSONRPC: "2.0", ID: id, Result: json.RawMessage(`null`)})
			if err != nil || json.Unmarshal(data, &resp) != nil || resp.ID != id {
				t.Fatalf("response round trip: %s, %v", data, err)
			}
		})
	}
	absent, err := json.Marshal(RPCRequest{JSONRPC: "2.0", Method: MethodHealth})
	if err != nil || bytes.Contains(absent, []byte(`"id"`)) {
		t.Fatalf("notification = %s, %v", absent, err)
	}
	null, err := json.Marshal(RPCResponse{JSONRPC: "2.0", Error: &RPCError{Code: ErrCodeParse, Message: "parse error"}})
	if err != nil || !bytes.Contains(null, []byte(`"id":null`)) {
		t.Fatalf("uncorrelated error = %s, %v", null, err)
	}
	for _, n := range []int64{maxRPCInteger + 1, -maxRPCInteger - 1} {
		if _, err := json.Marshal(NumberID(n)); err == nil {
			t.Fatalf("encoded unsafe ID %d", n)
		}
	}
}
func stringIDForTest(id RPCID) string { b, _ := json.Marshal(id); return string(b) }

func TestEnvelopeFaultClassification(t *testing.T) {
	for _, tc := range []struct {
		input string
		code  int
		id    RPCID
		reply bool
	}{
		{`{`, ErrCodeParse, RPCID{}, false},
		{`{"id":3,}`, ErrCodeParse, RPCID{}, false},
		{`[]`, ErrCodeInvalidRequest, RPCID{}, false},
		{`[{"jsonrpc":"2.0","id":3,"method":"plugin/health"}]`, ErrCodeInvalidRequest, RPCID{}, false},
		{`{"jsonrpc":"2.0","id":3,"id":4,"method":"plugin/health"}`, ErrCodeInvalidRequest, RPCID{}, false},
		{`{"jsonrpc":"2.0","id":"recover","method":1,"method":"plugin/health"}`, ErrCodeInvalidRequest, StringID("recover"), false},
		{`{"jsonrpc":"2.0","id":null,"method":"plugin/health"}`, ErrCodeInvalidRequest, RPCID{}, false},
		{`{"jsonrpc":"2.0","id":1e0,"method":"plugin/health"}`, ErrCodeInvalidRequest, RPCID{}, false},
		{`{"jsonrpc":"2.0","id":1.0,"method":"plugin/health"}`, ErrCodeInvalidRequest, RPCID{}, false},
		{`{"jsonrpc":"2.0","id":9007199254740992,"method":"plugin/health"}`, ErrCodeInvalidRequest, RPCID{}, false},
		{`{"jsonrpc":"2.0","id":5,"method":"plugin/health","result":{}}`, ErrCodeInvalidRequest, NumberID(5), false},
		{`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`, 0, RPCID{}, true},
		{`{"jsonrpc":"2.0","id":"other","result":null}`, 0, RPCID{}, true},
		{`{"jsonrpc":"2.0","id":null,"result":{}}`, ErrCodeInvalidRequest, RPCID{}, false},
		{`{"jsonrpc":"2.0","id":7,"error":null}`, ErrCodeInvalidRequest, NumberID(7), false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			req, fault := decodeEnvelope([]byte(tc.input))
			if req != nil {
				t.Fatalf("invalid/reply frame dispatched: %+v", req)
			}
			if tc.reply {
				if fault != nil {
					t.Fatalf("valid reply rejected: %+v", fault)
				}
				return
			}
			if fault == nil || fault.Error.Code != tc.code || fault.ID != tc.id {
				t.Fatalf("fault=%+v want code=%d id=%v", fault, tc.code, tc.id)
			}
		})
	}
	req, fault := decodeEnvelope([]byte(`{"jsonrpc":"2\u002e0","id":"\ud83d\ude00","method":"plugin/health"}`))
	if fault != nil || req == nil || req.ID != StringID("😀") {
		t.Fatalf("escaped version/id: req=%+v fault=%+v", req, fault)
	}
}

type envelopeCountingPlugin struct {
	transcriptBase
	calls int
}

func (p *envelopeCountingPlugin) Health(context.Context) (HealthStatus, error) {
	p.calls++
	return HealthStatus{OK: true}, nil
}
func TestInvalidEnvelopesNeverInvokeHandlers(t *testing.T) {
	p := &envelopeCountingPlugin{}
	init, err := json.Marshal(RPCRequest{JSONRPC: "2.0", ID: NumberID(8000), Method: MethodInit, Params: validInitParams()})
	if err != nil {
		t.Fatal(err)
	}
	var in bytes.Buffer
	in.Write(init)
	in.WriteByte('\n')
	for _, token := range []string{"null", "9007199254740993", "-9007199254740992", "1e0", "1.0", "true", "[]", "{}"} {
		in.WriteString(`{"jsonrpc":"2.0","id":` + token + `,"method":"plugin/health"}` + "\n")
	}
	in.WriteString(`{"jsonrpc":"2.0","id":1,"id":2,"method":"plugin/health"}` + "\n")
	in.WriteString(`{"jsonrpc":"2.0","id":"recover","method":1,"method":"plugin/health"}` + "\n")
	in.WriteString(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}` + "\n")
	var out bytes.Buffer
	if err := serveWith(p, &in, &out); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Fatalf("invalid frames invoked Health %d times", p.calls)
	}
	if got := strings.Count(out.String(), "\n"); got != 11 {
		t.Fatalf("reply count=%d want init + ten faults; %s", got, out.String())
	}
}

func TestEnvelopeLeavesPayloadPolicyToMethod(t *testing.T) {
	req, fault := decodeEnvelope([]byte(`{"jsonrpc":"2.0","id":1,"method":"http/handle","params":{"body":[1e0,2.0]}}`))
	if fault != nil || req == nil {
		t.Fatalf("envelope rejected payload: %+v", fault)
	}
	if _, err := validateRuntimeParams(req.Method, req.Params); err == nil {
		t.Fatal("method validator accepted byte-array body")
	}
}
