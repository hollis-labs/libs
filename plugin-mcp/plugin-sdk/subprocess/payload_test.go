package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"sort"
	"sync"
	"testing"
)

type payloadProbe struct {
	mu sync.Mutex
	transcriptFull
	calls    int
	contexts []string
}

func (p *payloadProbe) observe(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := ForwardContextFromContext(ctx)
	if !ok {
		p.contexts = append(p.contexts, "absent")
		return
	}
	b, _ := json.Marshal(c)
	p.contexts = append(p.contexts, string(b))
}
func (p *payloadProbe) Init(ctx context.Context, v InitParams) (InitResult, error) {
	p.observe(ctx)
	return p.transcriptFull.Init(ctx, v)
}
func (p *payloadProbe) Load(ctx context.Context) (LoadResult, error) {
	p.observe(ctx)
	return LoadResult{}, nil
}
func (p *payloadProbe) Unload(ctx context.Context) error { p.observe(ctx); return nil }
func (p *payloadProbe) Command(ctx context.Context, v CommandRequest) (CommandResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	p.observe(ctx)
	return CommandResult{Action: "noop"}, nil
}
func TestPayloadValidationFencesInvocationAndMetadata(t *testing.T) {
	p := &payloadProbe{}
	c := json.RawMessage(`{"timeout_ms":20}`)
	init := validInitParams()
	if err := json.Unmarshal(c, &init.Context); err != nil {
		t.Fatal(err)
	}
	replies := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: NumberID(1), Method: MethodInit, Params: init},
		{JSONRPC: "2.0", ID: NumberID(2), Method: MethodCommandExecute, Params: json.RawMessage(`{"name":"echo","args":"","session_id":"","context":{"timeout_ms":10}}`)},
		{JSONRPC: "2.0", ID: NumberID(3), Method: MethodCommandExecute, Params: json.RawMessage(`{"name":"echo","args":"","session_id":""}`)},
		{JSONRPC: "2.0", ID: NumberID(4), Method: MethodCommandExecute, Params: json.RawMessage(`{"name":"echo","args":"","session_id":"","context":{"timeout_ms":1,"parent_call":{}}}`)},
		{JSONRPC: "2.0", ID: NumberID(5), Method: MethodLoad, Params: json.RawMessage(`{"context":{"timeout_ms":3}}`)},
		{JSONRPC: "2.0", ID: NumberID(6), Method: MethodUnload, Params: json.RawMessage(`null`)},
		{JSONRPC: "2.0", ID: NumberID(7), Method: MethodUnload, Params: json.RawMessage(`{"context":{"timeout_ms":4}}`)},
	})
	for _, i := range []int{3, 5} {
		if replies[i].Error == nil || replies[i].Error.Code != ErrCodeInvalidParams {
			t.Fatalf("reply %d: %+v", i, replies[i])
		}
	}
	if p.calls != 2 {
		t.Fatalf("invalid params invoked callback: calls=%d", p.calls)
	}
	want := []string{`{"timeout_ms":20}`, `{"timeout_ms":10}`, "absent", `{"timeout_ms":3}`, `{"timeout_ms":4}`}
	if len(p.contexts) != len(want) {
		t.Fatalf("contexts %v", p.contexts)
	}
	sort.Strings(want)
	sort.Strings(p.contexts)
	for i, v := range want {
		if p.contexts[i] != v {
			t.Fatalf("contexts %v", p.contexts)
		}
	}
}

type unencodableCRUD struct {
	transcriptFull
	value map[string]interface{}
}

func (p *unencodableCRUD) Create(context.Context, string, map[string]interface{}) (map[string]interface{}, error) {
	return p.value, nil
}
func (p *unencodableCRUD) Read(context.Context, string, string) (map[string]interface{}, error) {
	return p.value, nil
}
func (p *unencodableCRUD) Update(context.Context, string, string, map[string]interface{}) (map[string]interface{}, error) {
	return p.value, nil
}
func (p *unencodableCRUD) List(context.Context, string, map[string]interface{}) ([]map[string]interface{}, error) {
	return []map[string]interface{}{p.value}, nil
}
func TestCRUDSerializationFailureIsRPCError(t *testing.T) {
	for _, value := range []map[string]interface{}{{"function": func() {}}, {"nan": math.NaN()}, {"unicode": string([]byte{0xff})}, {"duplicate": json.RawMessage(`{"a":1,"a":2}`)}} {
		p := &unencodableCRUD{value: value}
		for _, method := range []string{MethodCRUDCreate, MethodCRUDRead, MethodCRUDUpdate, MethodCRUDList} {
			params := json.RawMessage(`{"resource_type":"notes","id":"a","data":{},"filters":{}}`)
			r := drive(t, p, []RPCRequest{{JSONRPC: "2.0", ID: NumberID(1), Method: method, Params: params}})
			if len(r) != 1 || r[0].Error == nil || r[0].Error.Code != ErrCodeInternal {
				t.Fatalf("%s returned successful invalid JSON: %+v", method, r)
			}
		}
	}
}
func TestPayloadRawTokensAndOpaqueKeys(t *testing.T) {
	raw := json.RawMessage(`{"method":"GET","path":"/","body":"AA==","identity":{"MixedCase":1,"mixedcase":2}}`)
	if _, err := validateRuntimeParams(MethodHTTPHandle, raw); err != nil {
		t.Fatal(err)
	}
	var v HTTPRequest
	if err := decodeParams(raw, &v); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v.Identity, []byte(`{"MixedCase":1,"mixedcase":2}`)) {
		t.Fatalf("opaque raw value changed: %s", v.Identity)
	}
}

func TestCRUDSuppliedEmptyDataIsNotOmitted(t *testing.T) {
	params := CRUDParams{ResourceType: "notes", Data: map[string]interface{}{}}
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateRuntimeParams(MethodCRUDCreate, json.RawMessage(b)); err != nil {
		t.Fatalf("empty data lost: %s: %v", b, err)
	}
}

func TestRequiredObjectHostEncoding(t *testing.T) {
	for _, value := range []map[string]interface{}{nil, {}, {"MixedCase": "value"}} {
		event := EventHandleParams{Type: "post", Source: "host", Data: value, Identity: json.RawMessage(`{"subject":"caller"}`)}
		mcp := MCPCallRequest{ToolName: "echo", Arguments: value, Identity: json.RawMessage(`{"subject":"caller"}`)}
		for _, tc := range []struct {
			method, field string
			params        any
		}{{MethodEventHandle, "data", event}, {MethodMCPCallTool, "arguments", mcp}} {
			b, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateRuntimeParams(tc.method, json.RawMessage(b)); err != nil {
				t.Fatalf("host DTO rejected by plugin validator: %s: %v", b, err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(b, &fields); err != nil {
				t.Fatal(err)
			}
			want := `{}`
			if value != nil && len(value) > 0 {
				want = `{"MixedCase":"value"}`
			}
			if string(fields[tc.field]) != want {
				t.Fatalf("%s encoded %s, want %s", tc.field, fields[tc.field], want)
			}
			if string(fields["identity"]) != `{"subject":"caller"}` {
				t.Fatalf("other fields changed: %s", b)
			}
		}
		if value == nil && (event.Data != nil || mcp.Arguments != nil) {
			t.Fatal("encoding mutated caller's nil map")
		}
	}
}
