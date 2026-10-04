package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type negotiatedPeer struct {
	in   *io.PipeWriter
	out  *json.Decoder
	done chan error
}

func negotiationPeer(t *testing.T, p Plugin, opt bool) *negotiatedPeer {
	t.Helper()
	ir, iw := io.Pipe()
	or, ow := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeWithOptions(p, ServeOptions{ReverseRPC: opt, Input: ir, Output: ow, Context: ctx})
		ow.Close()
	}()
	t.Cleanup(func() { cancel(); iw.Close(); ir.Close(); or.Close(); ow.Close() })
	return &negotiatedPeer{iw, json.NewDecoder(or), done}
}
func (p *negotiatedPeer) send(t *testing.T, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.in.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
}
func (p *negotiatedPeer) read(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var v map[string]json.RawMessage
	if err := p.out.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
func (p *negotiatedPeer) finish(t *testing.T) {
	t.Helper()
	p.in.Close()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve retained connection")
	}
}
func negotiationInit(t *testing.T) InitParams {
	t.Helper()
	raw, err := os.ReadFile("../protocol/v2/fixtures/negotiation.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct{ Init json.RawMessage }
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var p InitParams
	if err = json.Unmarshal(doc.Cases[0].Init, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSharedReverseNegotiation(t *testing.T) {
	raw, err := os.ReadFile("../protocol/v2/fixtures/negotiation.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name        string
			Reverse     bool `json:"reverseRPC"`
			Authored    bool `json:"authored_ack"`
			InitClient  bool `json:"init_client"`
			Init        json.RawMessage
			Ack, Client bool
			Error       string `json:"init_error"`
		}
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, v := range doc.Cases {
		t.Run(v.Name, func(t *testing.T) {
			observed := make(chan bool, 1)
			plugin := &shutdownPlugin{initFn: func(ctx context.Context, p InitParams) (InitResult, error) {
				_, got := HostClientFromContext(ctx)
				if got != v.InitClient {
					t.Errorf("Init client=%v", got)
				}
				r, _ := (&basePlugin{}).Init(ctx, p)
				if v.Authored {
					one := 1
					r.ReverseRPCVersion = &one
				}
				return r, nil
			}, healthFn: func(ctx context.Context) (HealthStatus, error) {
				_, ok := HostClientFromContext(ctx)
				observed <- ok
				return HealthStatus{OK: true}, nil
			}}
			peer := negotiationPeer(t, plugin, v.Reverse)
			peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(7), Method: MethodInit, Params: v.Init})
			reply := peer.read(t)
			if v.Error != "" {
				var e RPCError
				if json.Unmarshal(reply["error"], &e) != nil || e.Code != -32602 {
					t.Fatalf("reply=%s", reply)
				}
				var data struct{ Code string }
				b, _ := json.Marshal(e.Data)
				json.Unmarshal(b, &data)
				if data.Code != v.Error {
					t.Fatalf("code=%s", data.Code)
				}
				peer.finish(t)
				return
			}
			var result InitResult
			if err = json.Unmarshal(reply["result"], &result); err != nil {
				t.Fatal(err)
			}
			if (result.ReverseRPCVersion != nil) != v.Ack {
				t.Fatalf("ack=%v", result.ReverseRPCVersion)
			}
			peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(8), Method: MethodHealth, Params: json.RawMessage(`{"context":{"binding_id":"binding-example","timeout_ms":10000}}`)})
			peer.read(t)
			if got := <-observed; got != v.Client {
				t.Fatalf("client=%v", got)
			}
			peer.finish(t)
		})
	}
}

func TestNegotiatedLifecycleLogsAndBoundCallback(t *testing.T) {
	init := negotiationInit(t)
	var saved *HostClient
	log := func(ctx context.Context) error {
		h, ok := HostClientFromContext(ctx)
		if !ok {
			return errors.New("missing lifecycle client")
		}
		saved = h
		if _, err := h.StorageGet(ctx, StorageGetArgs{"g-StorageGet", "k"}); err == nil {
			return errors.New("lifecycle business helper available")
		}
		_, err := h.Log(ctx, HostLogArgs{GrantID: "g-Log", Level: "info", Message: "lifecycle"})
		return err
	}
	plugin := &shutdownPlugin{initFn: func(ctx context.Context, p InitParams) (InitResult, error) {
		if err := log(ctx); err != nil {
			return InitResult{}, err
		}
		return (&basePlugin{}).Init(ctx, p)
	}, loadFn: func(ctx context.Context) (LoadResult, error) { return LoadResult{}, log(ctx) }, unloadFn: log, healthFn: func(ctx context.Context) (HealthStatus, error) {
		h, ok := HostClientFromContext(ctx)
		if !ok {
			return HealthStatus{}, errors.New("missing bound client")
		}
		r, err := h.StorageGet(ctx, StorageGetArgs{"g-StorageGet", "k"})
		return HealthStatus{OK: !r.Found}, err
	}}
	peer := negotiationPeer(t, plugin, true)
	methods := []string{MethodInit, MethodLoad, MethodHealth, MethodUnload}
	for i, method := range methods {
		id := int64(7 + i)
		params := any(json.RawMessage(`{"context":{"binding_id":"binding-example","timeout_ms":10000}}`))
		if method == MethodInit {
			params = init
		}
		peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(id), Method: method, Params: params})
		request := peer.read(t)
		var rpc RPCRequest
		b, _ := json.Marshal(request)
		if err := json.Unmarshal(b, &rpc); err != nil {
			t.Fatal(err)
		}
		expected := "host/log"
		result := any(map[string]bool{"accepted": true})
		if method == MethodHealth {
			expected = "host/storage/get"
			result = map[string]bool{"found": false}
		}
		if rpc.Method != expected {
			t.Fatalf("request=%s", b)
		}
		var paramsHost struct{ Context ReverseContext }
		json.Unmarshal(request["params"], &paramsHost)
		if paramsHost.Context.ParentCall.ID != uint64(id) || paramsHost.Context.ParentCall.RequestOwner != HostRPCOwnerHost || paramsHost.Context.TimeoutMS > 1000 || paramsHost.Context.TimeoutMS == 0 {
			t.Fatalf("context=%+v", paramsHost)
		}
		if method == MethodInit {
			peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(17), Method: MethodHealth, Params: json.RawMessage(`{}`)})
			refused := peer.read(t)
			if refused["error"] == nil {
				t.Fatal("pipelined callback executed")
			}
		}
		peer.send(t, RPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: mustJSON(t, result)})
		reply := peer.read(t)
		if reply["error"] != nil {
			t.Fatalf("reply=%s", reply)
		}
	}
	select {
	case err := <-peer.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Unload retained connection")
	}
	if _, err := saved.Log(context.Background(), HostLogArgs{GrantID: "g-Log", Level: "info", Message: "late"}); err == nil {
		t.Fatal("cached lifecycle client survived")
	}
}
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNegotiatedPolicyRejectsOversizedOutboundAndInbound(t *testing.T) {
	init := negotiationInit(t)
	l := &init.HostServices.Limits
	l.MaxFrameBytes = 1024
	l.MaxQueuedWriteBytes = 2048
	l.HostToPluginInflight = 1
	l.PluginToHostInflight = 1
	l.WriteTimeoutMS = 250
	l.MethodTimeoutMS["host/log"] = 200
	plugin := &shutdownPlugin{initFn: func(ctx context.Context, p InitParams) (InitResult, error) {
		h, ok := HostClientFromContext(ctx)
		if !ok {
			t.Error("missing provisional log")
		}
		a := h.scope.manager
		a.mu.Lock()
		limits := a.limits
		a.mu.Unlock()
		if limits.Forward != 1 || limits.Reverse != 1 {
			t.Errorf("limits=%+v", limits)
		}
		a.writer.mu.Lock()
		queue, timeout := a.writer.limits, a.writer.timeout
		a.writer.mu.Unlock()
		if queue.Bytes != 2048 || timeout != 250*time.Millisecond {
			t.Errorf("writer=%+v timeout=%s", queue, timeout)
		}
		_, err := h.Log(ctx, HostLogArgs{GrantID: "g-Log", Level: "info", Message: strings.Repeat("x", 1500)})
		var tooLarge *FrameTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Errorf("oversize=%v", err)
		}
		p.HostServices.Methods = nil
		p.HostServices.Limits.MethodTimeoutMS = nil
		p.Grants = nil // Cannot rewrite captured agreement/policy.
		return (&basePlugin{}).Init(ctx, p)
	}}
	peer := negotiationPeer(t, plugin, true)
	peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(7), Method: MethodInit, Params: init})
	r := peer.read(t)
	if r["error"] != nil {
		t.Fatalf("Init=%s", r)
	}
	peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(8), Method: MethodCommandExecute, Params: map[string]any{"name": "large", "session_id": "", "args": strings.Repeat("x", 1100)}})
	select {
	case err := <-peer.done:
		var large *FrameTooLargeError
		if !errors.As(err, &large) || large.Direction != "input" {
			t.Fatalf("failure=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("oversized input did not fence")
	}
}

func TestFailedInitRevokesOutstandingProvisionalLog(t *testing.T) {
	init := negotiationInit(t)
	release := make(chan struct{})
	logged := make(chan error, 1)
	var cached context.Context
	plugin := &shutdownPlugin{initFn: func(ctx context.Context, p InitParams) (InitResult, error) {
		cached = ctx
		h, ok := HostClientFromContext(ctx)
		if !ok {
			return InitResult{}, errors.New("missing client")
		}
		go func() {
			_, err := h.Log(ctx, HostLogArgs{GrantID: "g-Log", Level: "info", Message: "pending"})
			logged <- err
		}()
		<-release
		return InitResult{}, errors.New("Init failed")
	}}
	peer := negotiationPeer(t, plugin, true)
	peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(7), Method: MethodInit, Params: init})
	call := peer.read(t)
	if call["method"] == nil {
		t.Fatalf("no provisional request: %s", call)
	}
	// Read completion is not a whole-write receipt. Wait for the actual writer
	// selection to finish before testing an outstanding-reply revocation.
	writer := scopeFromContext(cached).manager.writer
	end := time.Now().Add(time.Second)
	for {
		writer.mu.Lock()
		idle := writer.activeBytes == 0
		writer.mu.Unlock()
		if idle {
			break
		}
		if time.Now().After(end) {
			t.Fatal("write receipt did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	failure := peer.read(t)
	if failure["error"] == nil {
		t.Fatal("failed Init acknowledged")
	}
	if _, ok := HostClientFromContext(cached); ok {
		t.Fatal("provisional client survived failed Init")
	}
	select {
	case err := <-logged:
		if err == nil {
			t.Fatal("outstanding log survived")
		}
	case <-time.After(time.Second):
		t.Fatal("log retained after decline")
	}
	var id RPCID
	json.Unmarshal(call["id"], &id)
	peer.send(t, RPCResponse{JSONRPC: "2.0", ID: id, Result: json.RawMessage(`{"accepted":true}`)})
	peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(8), Method: MethodHealth, Params: json.RawMessage(`{}`)})
	if peer.read(t)["error"] == nil {
		t.Fatal("late reply revived readiness")
	}
	peer.finish(t)
}

func TestActiveReverseMissingBindingEOFAndInitReplay(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF", true: "replay"}[reuse], func(t *testing.T) {
			plugin := &shutdownPlugin{healthFn: func(ctx context.Context) (HealthStatus, error) {
				if _, ok := HostClientFromContext(ctx); ok {
					t.Error("missing binding got client")
				}
				return HealthStatus{OK: true}, nil
			}, unloadFn: func(ctx context.Context) error {
				if _, ok := HostClientFromContext(ctx); ok {
					t.Error("EOF invented client")
				}
				return nil
			}}
			peer := negotiationPeer(t, plugin, true)
			peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(7), Method: MethodInit, Params: negotiationInit(t)})
			peer.read(t)
			id := int64(8)
			if reuse {
				id = 7
			}
			peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(id), Method: MethodHealth, Params: json.RawMessage(`{}`)})
			if !reuse {
				peer.read(t)
				peer.finish(t)
				return
			}
			select {
			case err := <-peer.done:
				if !errors.Is(err, errCorrelation) {
					t.Fatalf("replay=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Init high-water not seeded")
			}
		})
	}
}

type initIdentityDeadlinePlugin struct{ shutdownPlugin }

func (*initIdentityDeadlinePlugin) Identity(ctx context.Context, _ json.RawMessage) { <-ctx.Done() }

func TestInitTerminalFailureCannotActivateReverse(t *testing.T) {
	for _, identity := range []bool{false, true} {
		t.Run(map[bool]string{false: "output-fallback", true: "identity-deadline"}[identity], func(t *testing.T) {
			init := negotiationInit(t)
			var plugin Plugin = &shutdownPlugin{initFn: func(ctx context.Context, p InitParams) (InitResult, error) {
				r, _ := (&basePlugin{}).Init(ctx, p)
				r.Description = strings.Repeat("x", 1500)
				return r, nil
			}}
			if identity {
				init.Identity = json.RawMessage(`{}`)
				init.Context.TimeoutMS = 50
				plugin = &initIdentityDeadlinePlugin{}
			}
			if !identity {
				init.HostServices.Limits.MaxFrameBytes = 1024
				init.HostServices.Limits.MaxQueuedWriteBytes = 2048
			}
			peer := negotiationPeer(t, plugin, true)
			peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(7), Method: MethodInit, Params: init})
			failed := peer.read(t)
			if failed["error"] == nil {
				t.Fatalf("Init acknowledged after failure: %s", failed)
			}
			peer.send(t, RPCRequest{JSONRPC: "2.0", ID: NumberID(8), Method: MethodHealth, Params: json.RawMessage(`{}`)})
			var refusal RPCError
			json.Unmarshal(peer.read(t)["error"], &refusal)
			if refusal.Message != "successful init required" {
				t.Fatalf("active after failed terminal: %+v", refusal)
			}
			peer.finish(t)
		})
	}
}
