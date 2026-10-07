package subprocess

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type hostClientVector struct {
	Error                json.RawMessage
	Name, Helper, Method string
	Args, Result         json.RawMessage
	ExpectedError        string `json:"expected_error"`
}

func clientFixture(t *testing.T, init InitParams) (*HostClient, context.Context, *correlation, *requestScope) {
	t.Helper()
	core := newCorrelation(true)
	core.methodTimeoutMS = init.HostServices.Limits.MethodTimeoutMS
	s := &server{}
	core.encode = s.encodeFrame
	writer := newFrameWriter(io.Discard, time.Second, func() {})
	admission := newAdmission(writer, core, s)
	scope, err := admission.begin(context.Background(), RPCRequest{ID: NumberID(17), Method: MethodHealth, Params: json.RawMessage(`{"context":{"binding_id":"binding-example","timeout_ms":10000}}`)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := hostClientContext(scope.ctx, core, init, newSecretTracker(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	h, ok := HostClientFromContext(ctx)
	if !ok {
		t.Fatal("missing client")
	}
	t.Cleanup(func() { scope.finish(); core.close(nil); writer.abort(errConnectionClosed) })
	return h, ctx, core, scope
}
func invokeHostVector(h *HostClient, ctx context.Context, v hostClientVector) (any, error) {
	var args map[string]json.RawMessage
	_ = json.Unmarshal(v.Args, &args)
	args["context"] = json.RawMessage(`{"binding_id":"binding-example","timeout_ms":1000,"parent_call":{"request_owner":"host","id":17}}`)
	v.Args, _ = json.Marshal(args)
	switch v.Helper {
	case "storageGet":
		type plain StorageGetParams
		var p plain
		_ = json.Unmarshal(v.Args, &p)
		return h.StorageGet(ctx, StorageGetArgs{p.GrantID, p.Key})
	case "storagePut":
		type plain StoragePutParams
		var p plain
		_ = json.Unmarshal(v.Args, &p)
		return h.StoragePut(ctx, StoragePutArgs{p.GrantID, p.Key, p.Value, p.ExpectedRevision, p.OperationKey})
	case "storageDelete":
		type plain StorageDeleteParams
		var p plain
		_ = json.Unmarshal(v.Args, &p)
		return h.StorageDelete(ctx, StorageDeleteArgs{p.GrantID, p.Key, p.ExpectedRevision, p.OperationKey})
	case "secretsGet":
		type plain SecretsGetParams
		var p plain
		_ = json.Unmarshal(v.Args, &p)
		return h.SecretsGet(ctx, SecretsGetArgs{p.GrantID, p.SecretRef})
	case "egressRequest":
		type plain EgressRequestParams
		var p plain
		_ = json.Unmarshal(v.Args, &p)
		return h.EgressRequest(ctx, EgressRequestArgs{p.GrantID, p.Method, p.URL, p.Headers, p.BodyBase64, p.OperationKey})
	case "eventsPublish":
		type plain EventsPublishParams
		var p plain
		_ = json.Unmarshal(v.Args, &p)
		return h.EventsPublish(ctx, EventsPublishArgs{p.GrantID, p.EventName, p.Payload, p.OperationKey})
	case "log":
		type plain LogParams
		var p plain
		_ = json.Unmarshal(v.Args, &p)
		return h.Log(ctx, HostLogArgs{p.GrantID, p.Level, p.Message, p.Fields})
	}
	panic("fixture helper")
}
func TestSharedHostClientVectors(t *testing.T) {
	for _, name := range []string{"storage", "secrets", "egress", "events-log"} {
		raw, err := os.ReadFile("../protocol/v2/fixtures/host-" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Init  InitParams
			Cases []hostClientVector
		}
		if err = json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, v := range doc.Cases {
			t.Run(v.Name, func(t *testing.T) {
				h, ctx, core, _ := clientFixture(t, doc.Init)
				calls := 0
				core.publish = func(frame []byte, receipt func(error)) error {
					calls++
					req, fault := decodeEnvelope(frame)
					if fault != nil || req.Method != v.Method {
						t.Fatal("publication")
					}
					raw, _ := paramsJSON(req.Params)
					var params map[string]json.RawMessage
					_ = json.Unmarshal(raw, &params)
					var c ReverseContext
					if err = json.Unmarshal(params["context"], &c); err != nil || c.BindingID != "binding-example" || c.ParentCall.ID != 17 || c.ParentCall.RequestOwner != HostRPCOwnerHost || c.TimeoutMS == 0 || c.TimeoutMS > 1000 {
						t.Fatalf("context %+v: %v", c, err)
					}
					delete(params, "context")
					actual, _ := json.Marshal(params)
					var a, b any
					_ = json.Unmarshal(actual, &a)
					_ = json.Unmarshal(v.Args, &b)
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("args %s", actual)
					}
					response := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v.Result}
					if len(v.Error) > 0 {
						delete(response, "result")
						response["error"] = v.Error
					}
					reply, _ := json.Marshal(response)
					if e := core.reply(reply); e != nil {
						core.close(e)
					}
					receipt(nil)
					return nil
				}
				got, err := invokeHostVector(h, ctx, v)
				if strings.HasPrefix(v.ExpectedError, "host:") {
					var fault *HostRPCError
					if !errors.As(err, &fault) || string(fault.Data.Code) != strings.TrimPrefix(v.ExpectedError, "host:") {
						t.Fatalf("host fault %v", err)
					}
					var expected HostRPCError
					_ = json.Unmarshal(v.Error, &expected)
					if !reflect.DeepEqual(fault.Data, expected.Data) {
						t.Fatal("lost host detail")
					}
					return
				}
				if v.ExpectedError == "invalid_input" {
					if err == nil || calls != 0 {
						t.Fatal("invalid input transmitted")
					}
					return
				}
				if v.ExpectedError != "" {
					var e *capability.Error
					if !errors.As(err, &e) || string(e.Code) != v.ExpectedError {
						t.Fatalf("expected %s, got %v", v.ExpectedError, err)
					}
					if v.ExpectedError == "capability_denied" && calls != 0 {
						t.Fatal("unauthorized publication")
					}
					return
				}
				if err != nil || calls != 1 {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
				if v.Helper == "secretsGet" {
					var expected SecretsGetResult
					_ = json.Unmarshal(v.Result, &expected)
					secret := got.(SecretValue)
					want, _ := base64.StdEncoding.DecodeString(expected.ValueBase64)
					if !reflect.DeepEqual(secret.Value, want) || secret.ExpiresAt != expected.ExpiresAt {
						t.Fatal("secret result")
					}
					redact, _ := h.secrets.redactor()
					if len(want) > 0 && (redact(expected.ValueBase64) == expected.ValueBase64 || redact(string(want)) == string(want)) {
						t.Fatal("secret exposure before registration")
					}
					return
				}
				encoded, _ := json.Marshal(got)
				var actual, expected any
				_ = json.Unmarshal(encoded, &actual)
				_ = json.Unmarshal(v.Result, &expected)
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("result %s", encoded)
				}
			})
		}
	}
}

func hostClientInitFixture(t *testing.T) InitParams {
	t.Helper()
	raw, err := os.ReadFile("../protocol/v2/fixtures/host-storage.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct{ Init InitParams }
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d.Init
}
func TestHostClientScopesBudgetsAndRefusals(t *testing.T) {
	init := hostClientInitFixture(t)
	h, ctx, core, scope := clientFixture(t, init)
	if _, ok := HostClientFromContext(context.Background()); ok {
		t.Fatal("base accessor")
	}
	init.Grants[0].Name = capability.SecretsRead // Client owns the Init snapshot.
	core.publish = func(frame []byte, done func(error)) error {
		req, _ := decodeEnvelope(frame)
		raw, _ := paramsJSON(req.Params)
		var p StorageGetParams
		_ = json.Unmarshal(raw, &p)
		if p.Context.TimeoutMS > 250 || p.Context.TimeoutMS == 0 {
			t.Fatal("caller budget extended")
		}
		reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32010, "message": "safe refusal", "data": map[string]any{"contract": "host-rpc/1", "code": "capability_denied", "effect_state": "not_started", "retryable": false, "request_id": req.ID}}})
		if err := core.reply(reply); err != nil {
			core.close(err)
		}
		done(nil)
		return nil
	}
	caller, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, err := h.StorageGet(caller, StorageGetArgs{"g-StorageGet", "key"})
	var fault *HostRPCError
	if !errors.As(err, &fault) || fault.Data.Code != capability.CapabilityDenied {
		t.Fatalf("typed refusal %v", err)
	}
	scope.mu.Lock()
	scope.terminal = true
	scope.mu.Unlock()
	if _, ok := HostClientFromContext(ctx); ok {
		t.Fatal("cached accessor retained authority")
	}
	_, err = h.StorageGet(ctx, StorageGetArgs{"g-StorageGet", "key"})
	var local *capability.Error
	if !errors.As(err, &local) || local.Code != capability.TargetUnavailable {
		t.Fatal("closed scope")
	}
}
func TestHostClientExpiredGrantAndPrivateBinding(t *testing.T) {
	for _, binding := range []bool{false, true} {
		t.Run(map[bool]string{false: "grant", true: "binding"}[binding], func(t *testing.T) {
			init := hostClientInitFixture(t)
			if !binding {
				init.Grants[0].ExpiresAt = "2001-01-01T00:00:00Z"
			}
			h, ctx, core, _ := clientFixture(t, init)
			if binding {
				h.scope.initLease(time.Now().Add(-time.Second))
			}
			core.publish = func([]byte, func(error)) error { t.Fatal("expired authority transmitted"); return nil }
			_, err := h.StorageGet(ctx, StorageGetArgs{"g-StorageGet", "key"})
			var deadline *DeadlineExceededError
			if !errors.As(err, &deadline) {
				t.Fatalf("expiry %v", err)
			}
		})
	}
}
func TestHostClientLogRedactionAndPublishedCancellation(t *testing.T) {
	h, ctx, core, scope := clientFixture(t, hostClientInitFixture(t))
	h.secrets.add("sensitive")
	core.publish = func(frame []byte, done func(error)) error {
		req, _ := decodeEnvelope(frame)
		raw, _ := paramsJSON(req.Params)
		var p LogParams
		_ = json.Unmarshal(raw, &p)
		if p.Message != "use [REDACTED]" || (*p.Fields)[0].Name != "[REDACTED]" || string((*p.Fields)[0].Value) != `{"nested":["[REDACTED]","[REDACTED]"]}` {
			t.Fatalf("unsafe log: %s", raw)
		}
		reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]bool{"accepted": true}})
		if err := core.reply(reply); err != nil {
			core.close(err)
		}
		done(nil)
		return nil
	}
	fields := []HostRPCLogField{{Name: "sensitive", Value: json.RawMessage(`{"nested":["sensitive","c2Vuc2l0aXZl"]}`)}}
	if _, err := h.Log(ctx, HostLogArgs{"g-Log", "info", "use sensitive", &fields}); err != nil {
		t.Fatal(err)
	}
	transmitted := make(chan struct{})
	core.publishCall = func(_ context.Context, _ []byte, done func(error), start func(), _ func() bool, _ func() ([]byte, error)) error {
		start()
		done(nil)
		close(transmitted)
		return nil
	}
	core.publishControl = func([]byte, func(error)) error { return nil }
	caller, cancel := context.WithCancelCause(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := h.StoragePut(caller, StoragePutArgs{GrantID: "g-StoragePut", Key: "k", Value: json.RawMessage(`{}`), OperationKey: "op"})
		result <- err
	}()
	<-transmitted
	scope.finish()
	select {
	case <-scope.executionDone:
		t.Fatal("parent released with pending child")
	default:
	}
	cancel(&TransportCancelledError{Reason: CallerCancelled})
	var transport *RPCTransportError
	if err := <-result; !errors.As(err, &transport) || transport.Failure.Code != capability.UnknownOutcome || transport.Failure.EffectState != capability.Unknown {
		t.Fatalf("mutation cancellation %v", err)
	}
	<-scope.executionDone
}

type noHostHooksPlugin struct {
	basePlugin
	checks chan bool
}

func (p *noHostHooksPlugin) Health(ctx context.Context) (HealthStatus, error) {
	_, ok := HostClientFromContext(ctx)
	p.checks <- ok
	return HealthStatus{OK: true}, nil
}
func (p *noHostHooksPlugin) HookHandle(ctx context.Context, r HookHandleParams) (HookHandleResult, error) {
	_, ok := HostClientFromContext(ctx)
	p.checks <- ok
	return HookHandleResult{InvocationID: r.InvocationID, Status: "ok"}, nil
}
func TestProductionBaseAndHooksExposeNoHostClient(t *testing.T) {
	for _, hooks := range []bool{false, true} {
		t.Run(map[bool]string{false: "base", true: "hooks"}[hooks], func(t *testing.T) {
			p := &noHostHooksPlugin{checks: make(chan bool, 2)}
			r := newDuplexRig(t, p, newCorrelation(false))
			init := validInitParams()
			init.Incarnation = capability.RuntimeIdentity{HostInstance: "fixture-host", OwnerID: "fixture", OwnerGeneration: 1}
			if hooks {
				init.HooksProfile = &HooksProfile{HooksProfileVersion: 1}
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": MethodInit, "params": init})
			r.send(string(raw))
			if response := r.read(); response["error"] != nil {
				t.Fatal(string(response["error"]))
			}
			r.send(`{"jsonrpc":"2.0","id":2,"method":"plugin/health","params":{"context":{"binding_id":"b","timeout_ms":10000}}}`)
			r.read()
			if <-p.checks {
				t.Fatal("production health host client")
			}
			if hooks {
				raw, err := os.ReadFile("../docs/protocol/v2/transcripts/hooks-negotiated.json")
				if err != nil {
					t.Fatal(err)
				}
				var fixture transcript
				if json.Unmarshal(raw, &fixture) != nil {
					t.Fatal("hooks fixture")
				}
				sent := false
				for _, step := range fixture.Steps {
					var req RPCRequest
					if json.Unmarshal(step.Send, &req) == nil && req.Method == MethodHookHandle {
						req.ID = NumberID(3)
						b, _ := json.Marshal(req)
						r.send(string(b))
						response := r.read()
						if response["error"] != nil {
							t.Fatal(string(response["error"]))
						}
						if <-p.checks {
							t.Fatal("production hook host client")
						}
						sent = true
						break
					}
				}
				if !sent {
					t.Fatal("no hook fixture")
				}
			}
			r.input.Close()
			if err := <-r.done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestHostClientUnavailableOfferAndDescriptor(t *testing.T) {
	for _, which := range []string{"offer", "descriptor", "hook", "lifecycle"} {
		t.Run(which, func(t *testing.T) {
			init := hostClientInitFixture(t)
			if which == "offer" {
				delete(init.HostServices.Limits.MethodTimeoutMS, "host/storage/get")
				methods := init.HostServices.Methods[:0]
				for _, method := range init.HostServices.Methods {
					if method != "host/storage/get" {
						methods = append(methods, method)
					}
				}
				init.HostServices.Methods = methods
			}
			if which == "descriptor" {
				init.Grants[0].Name = capability.StorageWrite
			}
			h, ctx, core, scope := clientFixture(t, init)
			core.publish = func([]byte, func(error)) error { t.Fatal("unavailable helper published"); return nil }
			if which == "hook" {
				scope.method = MethodHookHandle
			}
			if which == "lifecycle" {
				scope.method = MethodLoad
			}
			_, err := h.StorageGet(ctx, StorageGetArgs{"g-StorageGet", "key"})
			var local *capability.Error
			if !errors.As(err, &local) || local.EffectState != capability.NotStarted {
				t.Fatalf("unavailable call %v", err)
			}
		})
	}
}
func TestHostClientReceiptUsesPublishedOperationKey(t *testing.T) {
	h, ctx, core, _ := clientFixture(t, hostClientInitFixture(t))
	sent := make(chan *RPCRequest, 1)
	core.publish = func(frame []byte, done func(error)) error {
		req, _ := decodeEnvelope(frame)
		sent <- req
		done(nil)
		return nil
	}
	key := "original"
	result := make(chan error, 1)
	go func() {
		_, err := h.EgressRequest(ctx, EgressRequestArgs{GrantID: "g-EgressRequest", Method: "POST", URL: "https://example.com/", OperationKey: &key})
		result <- err
	}()
	req := <-sent
	key = "changed-after-publication"
	reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"status": 200, "headers": []any{}, "body_base64": "", "operation_key": "original"}})
	if err := core.reply(reply); err != nil {
		core.close(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
