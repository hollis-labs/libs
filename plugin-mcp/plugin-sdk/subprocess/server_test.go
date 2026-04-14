package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	plugin "github.com/hollis-labs/plugin-sdk"
)

// --- Test plugins exercising each capability interface ---

type basePlugin struct {
	id      string
	name    string
	version string
}

func (p *basePlugin) Init(ctx context.Context, params InitParams) (InitResult, error) {
	return InitResult{ID: p.id, Name: p.name, Version: p.version, Description: "test", Protocol: ProtocolVersion}, nil
}
func (p *basePlugin) Load(ctx context.Context) (LoadResult, error) { return LoadResult{}, nil }
func (p *basePlugin) Unload(ctx context.Context) error             { return nil }

type commandPlugin struct{ basePlugin }

func (p *commandPlugin) Command(ctx context.Context, req CommandRequest) (CommandResult, error) {
	return CommandResult{Action: "message", Content: "hello " + req.Args}, nil
}

type eventPlugin struct{ basePlugin }

func (p *eventPlugin) EventHandle(ctx context.Context, req EventRequest) (EventResult, error) {
	if req.Type == "veto" {
		return EventResult{Cancel: true, Reason: "no"}, nil
	}
	return EventResult{}, nil
}

type crudPlugin struct{ basePlugin }

func (p *crudPlugin) Create(ctx context.Context, rt string, d map[string]interface{}) (map[string]interface{}, error) {
	d["_created"] = true
	return d, nil
}
func (p *crudPlugin) Read(ctx context.Context, rt, id string) (map[string]interface{}, error) {
	if id == "missing" {
		return nil, plugin.ErrNotFound("no such " + rt)
	}
	return map[string]interface{}{"id": id, "rt": rt}, nil
}
func (p *crudPlugin) Update(ctx context.Context, rt, id string, d map[string]interface{}) (map[string]interface{}, error) {
	return d, nil
}
func (p *crudPlugin) Delete(ctx context.Context, rt, id string) error { return nil }
func (p *crudPlugin) List(ctx context.Context, rt string, f map[string]interface{}) ([]map[string]interface{}, error) {
	return []map[string]interface{}{{"id": "x"}}, nil
}

// --- Helpers ---

// drive runs serveWith against a scripted list of RPCRequest values
// written to an in-memory pipe, and returns the collected responses.
// It closes the input writer after all requests are written so Serve
// exits cleanly.
func drive(t *testing.T, p Plugin, reqs []RPCRequest) []RPCResponse {
	t.Helper()
	in, inW := io.Pipe()
	var out bytes.Buffer
	var outMu sync.Mutex

	// Wrap out in a sync writer so Serve's writeMu + our reads don't
	// race on the buffer.
	outW := &syncWriter{buf: &out, mu: &outMu}

	done := make(chan error, 1)
	go func() {
		done <- serveWith(p, in, outW)
	}()

	// Feed requests.
	for _, r := range reqs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal req: %v", err)
		}
		line = append(line, '\n')
		if _, err := inW.Write(line); err != nil {
			t.Fatalf("write req: %v", err)
		}
	}
	_ = inW.Close() // signals EOF → Serve returns

	if err := <-done; err != nil {
		t.Fatalf("serveWith: %v", err)
	}

	outMu.Lock()
	defer outMu.Unlock()
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	var resps []RPCResponse
	for _, l := range lines {
		if l == "" {
			continue
		}
		var r RPCResponse
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("bad response line %q: %v", l, err)
		}
		resps = append(resps, r)
	}
	// Responses are produced concurrently — sort by ID so test
	// assertions on resps[N] are deterministic.
	sort.Slice(resps, func(i, j int) bool { return resps[i].ID < resps[j].ID })
	return resps
}

type syncWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (w *syncWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(b)
}

// --- Tests ---

func TestServe_InitLoadUnload(t *testing.T) {
	p := &basePlugin{id: "test", name: "Test", version: "0.0.1"}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodInit, Params: InitParams{PluginDir: "/tmp", HostInfo: HostInfo{Version: "test", Protocol: 1}}},
		{JSONRPC: "2.0", ID: 2, Method: MethodLoad},
		{JSONRPC: "2.0", ID: 3, Method: MethodUnload},
	})
	if len(resps) != 3 {
		t.Fatalf("got %d responses, want 3", len(resps))
	}
	for _, r := range resps {
		if r.Error != nil {
			t.Errorf("id=%d got error: %+v", r.ID, r.Error)
		}
	}

	var init InitResult
	if err := json.Unmarshal(resps[0].Result, &init); err != nil {
		t.Fatalf("init result: %v", err)
	}
	if init.ID != "test" || init.Protocol != 1 {
		t.Errorf("init result = %+v", init)
	}
}

func TestServe_MethodNotFoundForMissingCapability(t *testing.T) {
	p := &basePlugin{id: "test"}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodCommandExecute, Params: CommandExecParams{Name: "x"}},
	})
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	if resps[0].Error == nil || resps[0].Error.Code != ErrCodeMethodNotFound {
		t.Errorf("expected method-not-found, got %+v", resps[0].Error)
	}
}

func TestServe_CommandHandler(t *testing.T) {
	p := &commandPlugin{basePlugin: basePlugin{id: "cmd"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodCommandExecute,
			Params: CommandExecParams{Name: "greet", Args: "world"}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	var res CommandExecResult
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Action != "message" || res.Content != "hello world" {
		t.Errorf("result = %+v", res)
	}
}

func TestServe_CommandEnvelopesPropagate(t *testing.T) {
	p := &envelopeCommandPlugin{basePlugin: basePlugin{id: "env"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodCommandExecute,
			Params: CommandExecParams{Name: "emit"}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	var res CommandExecResult
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Envelopes) != 1 || res.Envelopes[0].Type != "demo.card" {
		t.Errorf("envelopes did not propagate: %+v", res.Envelopes)
	}
}

type envelopeCommandPlugin struct{ basePlugin }

func (p *envelopeCommandPlugin) Command(ctx context.Context, req CommandRequest) (CommandResult, error) {
	return CommandResult{
		Action:    "message",
		Content:   "ok",
		Envelopes: []plugin.EnvelopeOut{{Type: "demo.card", Data: map[string]interface{}{"k": "v"}}},
	}, nil
}

func TestServe_EventEnvelopesPropagate(t *testing.T) {
	p := &envelopeEventPlugin{basePlugin: basePlugin{id: "env-evt"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodEventHandle,
			Params: EventHandleParams{Type: "message.sent"}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	var res EventHandleResult
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Envelopes) != 1 || res.Envelopes[0].Type != "demo.trace" {
		t.Errorf("event envelopes did not propagate: %+v", res.Envelopes)
	}
}

type envelopeEventPlugin struct{ basePlugin }

func (p *envelopeEventPlugin) EventHandle(ctx context.Context, req EventRequest) (EventResult, error) {
	return EventResult{
		Envelopes: []plugin.EnvelopeOut{{Type: "demo.trace", Data: map[string]interface{}{"seen": true}}},
	}, nil
}

func TestServe_EventHandlerCancel(t *testing.T) {
	p := &eventPlugin{basePlugin: basePlugin{id: "evt"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodEventHandle, Params: EventHandleParams{Type: "veto", PreHook: true}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	var res EventHandleResult
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.Cancel {
		t.Errorf("expected Cancel=true, got %+v", res)
	}
}

func TestServe_CRUDErrorMapping(t *testing.T) {
	p := &crudPlugin{basePlugin: basePlugin{id: "crud"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodCRUDRead, Params: CRUDParams{ResourceType: "widget", ID: "missing"}},
		{JSONRPC: "2.0", ID: 2, Method: MethodCRUDRead, Params: CRUDParams{ResourceType: "widget", ID: "found"}},
	})
	if len(resps) != 2 {
		t.Fatalf("got %d", len(resps))
	}
	if resps[0].Error == nil || resps[0].Error.Code != ErrCodeNotFound {
		t.Errorf("expected NotFound, got %+v", resps[0].Error)
	}
	if resps[1].Error != nil {
		t.Errorf("unexpected error on found read: %+v", resps[1].Error)
	}
}

func TestServe_PanicRecovery(t *testing.T) {
	p := &panicPlugin{basePlugin: basePlugin{id: "panic"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodCommandExecute, Params: CommandExecParams{Name: "boom"}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	if resps[0].Error == nil || resps[0].Error.Code != ErrCodeInternal {
		t.Errorf("expected internal error from panic, got %+v", resps[0].Error)
	}
	if !strings.Contains(resps[0].Error.Message, "panic") {
		t.Errorf("message missing 'panic': %q", resps[0].Error.Message)
	}
}

type panicPlugin struct{ basePlugin }

func (p *panicPlugin) Command(ctx context.Context, req CommandRequest) (CommandResult, error) {
	panic(fmt.Errorf("boom"))
}

// --- MCP / HTTP / Migrate dispatch tests (v0.3.0) ---

type mcpPlugin struct {
	basePlugin
	gotReq MCPCallRequest
}

func (p *mcpPlugin) MCPCallTool(ctx context.Context, req MCPCallRequest) (MCPCallResult, error) {
	p.gotReq = req
	if req.ToolName == "boom" {
		return MCPCallResult{}, fmt.Errorf("tool failed")
	}
	return MCPCallResult{
		Content:   json.RawMessage(`{"ok":true}`),
		Envelopes: []plugin.EnvelopeOut{{Type: "demo.result", Data: map[string]interface{}{"k": "v"}}},
	}, nil
}

func TestServe_MCPHandler(t *testing.T) {
	p := &mcpPlugin{basePlugin: basePlugin{id: "mcp"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodMCPCallTool,
			Params: MCPCallRequest{ToolName: "search", Arguments: map[string]interface{}{"q": "cats"}, SessionID: "s1"}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	if resps[0].Error != nil {
		t.Fatalf("unexpected error: %+v", resps[0].Error)
	}
	var res MCPCallResult
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(res.Content) != `{"ok":true}` {
		t.Errorf("content = %s", res.Content)
	}
	if len(res.Envelopes) != 1 || res.Envelopes[0].Type != "demo.result" {
		t.Errorf("envelopes did not propagate: %+v", res.Envelopes)
	}
	if p.gotReq.ToolName != "search" || p.gotReq.SessionID != "s1" {
		t.Errorf("plugin did not receive request: %+v", p.gotReq)
	}
}

func TestServe_MCPMethodNotFound(t *testing.T) {
	p := &basePlugin{id: "none"}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodMCPCallTool, Params: MCPCallRequest{ToolName: "x"}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	if resps[0].Error == nil || resps[0].Error.Code != ErrCodeMethodNotFound {
		t.Errorf("expected method-not-found, got %+v", resps[0].Error)
	}
}

type httpPlugin struct{ basePlugin }

func (p *httpPlugin) HTTPHandle(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	if req.Method == "GET" && req.Path == "/ping" {
		return HTTPResponse{Status: 200, Headers: map[string]string{"X-Echo": req.Query["msg"]}, Body: []byte("pong")}, nil
	}
	return HTTPResponse{Status: 404}, nil
}

func TestServe_HTTPHandler(t *testing.T) {
	p := &httpPlugin{basePlugin: basePlugin{id: "http"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodHTTPHandle,
			Params: HTTPRequest{Method: "GET", Path: "/ping", Query: map[string]string{"msg": "hi"}}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	if resps[0].Error != nil {
		t.Fatalf("unexpected error: %+v", resps[0].Error)
	}
	var res HTTPResponse
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Status != 200 || string(res.Body) != "pong" || res.Headers["X-Echo"] != "hi" {
		t.Errorf("response = %+v", res)
	}
}

func TestServe_HTTPMethodNotFound(t *testing.T) {
	p := &basePlugin{id: "none"}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodHTTPHandle, Params: HTTPRequest{Method: "GET", Path: "/x"}},
	})
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != ErrCodeMethodNotFound {
		t.Errorf("expected method-not-found, got %+v", resps)
	}
}

type migratePlugin struct {
	basePlugin
	gotFrom, gotTo string
}

func (p *migratePlugin) Migrate(ctx context.Context, from, to string) error {
	p.gotFrom = from
	p.gotTo = to
	if to == "bad" {
		return fmt.Errorf("migration failed")
	}
	return nil
}

func TestServe_Migrator(t *testing.T) {
	p := &migratePlugin{basePlugin: basePlugin{id: "mig"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodMigrate, Params: MigrateParams{FromVersion: "0.1.0", ToVersion: "0.2.0", DataDir: "/tmp"}},
	})
	if len(resps) != 1 {
		t.Fatalf("got %d", len(resps))
	}
	if resps[0].Error != nil {
		t.Fatalf("unexpected error: %+v", resps[0].Error)
	}
	if p.gotFrom != "0.1.0" || p.gotTo != "0.2.0" {
		t.Errorf("plugin did not receive params: from=%q to=%q", p.gotFrom, p.gotTo)
	}
}

func TestServe_MigrateError(t *testing.T) {
	p := &migratePlugin{basePlugin: basePlugin{id: "mig"}}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodMigrate, Params: MigrateParams{FromVersion: "0.1.0", ToVersion: "bad"}},
	})
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != ErrCodeInternal {
		t.Errorf("expected internal error, got %+v", resps)
	}
}

func TestServe_MigrateMethodNotFound(t *testing.T) {
	p := &basePlugin{id: "none"}
	resps := drive(t, p, []RPCRequest{
		{JSONRPC: "2.0", ID: 1, Method: MethodMigrate, Params: MigrateParams{FromVersion: "0.1.0", ToVersion: "0.2.0"}},
	})
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != ErrCodeMethodNotFound {
		t.Errorf("expected method-not-found, got %+v", resps)
	}
}

func TestServe_NilPlugin(t *testing.T) {
	err := Serve(nil)
	if err == nil {
		t.Fatal("expected error for nil plugin")
	}
}

func TestServe_ParseError(t *testing.T) {
	in, inW := io.Pipe()
	var out bytes.Buffer
	var mu sync.Mutex
	outW := &syncWriter{buf: &out, mu: &mu}

	p := &basePlugin{id: "x"}
	done := make(chan error, 1)
	go func() { done <- serveWith(p, in, outW) }()

	_, _ = inW.Write([]byte("this is not json\n"))
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("serveWith: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(out.String(), "parse error") {
		t.Errorf("expected parse error response, got %q", out.String())
	}
}
