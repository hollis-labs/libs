package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
	sdk "github.com/hollis-labs/plugin-sdk/subprocess"
)

type vector struct {
	Name, DTO, Raw string
	Valid          bool
	Notification   *bool
	Request        *sdk.HookHandleParams
}

func corpus(t *testing.T) []vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("SDK_TEST_SOURCE"), "protocol/v2/fixtures/hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}
func TestPinnedCorpus(t *testing.T) {
	for _, v := range corpus(t) {
		t.Run(v.Name, func(t *testing.T) {
			err := sdk.ValidateHookDTO(v.DTO, []byte(v.Raw))
			if err == nil && v.Notification != nil {
				if v.DTO == "HookHandleParams" {
					p, e := sdk.DecodeHookHandleParams([]byte(v.Raw))
					err = e
					if err == nil {
						err = sdk.ValidateHookRequest(p, *v.Notification)
					}
				} else {
					p, e := sdk.DecodeHookHandleBatchParams([]byte(v.Raw))
					err = e
					if err == nil {
						err = sdk.ValidateHookBatchRequest(p, *v.Notification)
					}
				}
			}
			if err == nil && v.Request != nil {
				r, e := sdk.DecodeHookHandleResult([]byte(v.Raw))
				err = e
				if err == nil {
					err = sdk.ValidateHookResultFor(*v.Request, r)
				}
				mapped, e := Result(*v.Request, []byte(v.Raw))
				if e != nil {
					t.Fatal(e)
				}
				if !v.Valid && (mapped.Failure == nil || mapped.Failure.Code != hooks.InvalidOutput) {
					t.Fatal("invalid result escaped bridge")
				}
			}
			if v.DTO == "HookHandleResult" && v.Valid {
				var result sdk.HookHandleResult
				if e := json.Unmarshal([]byte(v.Raw), &result); e != nil {
					t.Fatal(e)
				}
				if result.Error != nil {
					f, e := Failure(*result.Error)
					if e != nil {
						t.Fatal(e)
					}
					wire, e := WireFailure(f)
					if e != nil || wire.Code != result.Error.Code {
						t.Fatalf("failure code lost: %+v %v", wire, e)
					}
				}
			}
			if (err == nil) != v.Valid {
				t.Fatalf("valid=%v error=%v", v.Valid, err)
			}
			if v.DTO == "HookFailure" && v.Valid {
				var f sdk.HookFailure
				if err = json.Unmarshal([]byte(v.Raw), &f); err != nil {
					t.Fatal(err)
				}
				mapped, e := Failure(f)
				if e != nil {
					t.Fatal(e)
				}
				wire, e := WireFailure(mapped)
				if e != nil || wire.Code != f.Code {
					t.Fatalf("failure round trip: %+v %v", wire, e)
				}
			}
		})
	}
}
func TestPinnedTranscripts(t *testing.T) {
	// Use the SDK's authored replay runners against their OWN pinned corpus.
	// No transcript or literal vector is copied into this repository.
	source := os.Getenv("SDK_TEST_SOURCE")
	if source == "" {
		t.Fatal("prepare pinned test source first")
	}
	for _, runtime := range []string{"Go", "Node"} {
		t.Run(runtime, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var cmd *exec.Cmd
			if runtime == "Go" {
				cmd = exec.CommandContext(ctx, os.Getenv("SDK_TEST_GO_CHILD"), "-test.run=^TestHookRealChildTranscripts$", "-test.v")
				cmd.Dir = filepath.Join(source, "subprocess")
			} else {
				cmd = exec.CommandContext(ctx, "node", "--test", "--test-name-pattern=real Node hook transcript", filepath.Join(source, "ts/packages/plugin-sdk/test/hooks.test.js"))
				cmd.Dir = source
			}
			cmd.Env = append(os.Environ(), "HOOK_TEST_CHILD=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !bytes.Contains(out, []byte("hooks-handling.json")) || !bytes.Contains(out, []byte("hooks-declined.json")) {
				t.Fatal("SDK replay did not report both transcripts")
			}
			t.Log(string(out))
		})
	}
}
func hostRequest(p sdk.HookHandleParams) hooks.RemoteRequest {
	q := hooks.RemoteRequest{InvocationID: p.InvocationID, CatalogVersion: p.CatalogVersion, Hook: p.Hook, SchemaDigest: p.SchemaDigest, Kind: hooks.Kind(p.Kind), Mode: hooks.Mode(p.Mode), Scope: hooks.RemoteScope{HostInstance: p.Scope.Incarnation.HostInstance, Owner: p.Scope.Incarnation.OwnerID, Generation: "opaque-host-generation", RegistrationID: p.Scope.RegistrationID}, Payload: bytes.Clone(p.Payload), Metadata: cloneMetadata(p.Metadata)}
	deadline, _ := time.Parse(time.RFC3339Nano, p.Deadline)
	q.Context = hooks.RemoteContext{Deadline: deadline, Timeout: time.Duration(p.Context.TimeoutMS) * time.Millisecond, AggregateBudget: time.Duration(p.AggregateBudgetMS) * time.Millisecond, Depth: int(p.Depth), Trace: hooks.TraceContext{TraceID: p.Trace.TraceID, SpanID: p.Trace.SpanID}, RootInvocationID: p.RootInvocationID, ConnectionID: "host-ledger"}
	if p.Context.BindingID != nil {
		q.Context.BindingID = string(*p.Context.BindingID)
	}
	if p.ParentInvocationID != nil {
		q.Context.ParentInvocationID = *p.ParentInvocationID
	}
	return q
}
func TestHostLedgerAndLiteralMappings(t *testing.T) {
	p, err := sdk.DecodeHookHandleParams([]byte(corpus(t)[0].Raw))
	if err != nil {
		t.Fatal(err)
	}
	q := hostRequest(p)
	for _, literal := range []string{`null`, `{
 "n":1.50,"large":9007199254740993,"x\u005b":"<x>&"
}`} {
		q.Payload = []byte(literal)
		wire, e := Request(q, p.Scope.Incarnation)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := sdk.EncodeHookHandleParams(wire)
		if e != nil || !bytes.Contains(raw, q.Payload) {
			t.Fatalf("literal changed: %s %v", raw, e)
		}
		restored, e := RestoreRequest(wire, q, p.Scope.Incarnation)
		if e != nil || !bytes.Equal(restored.Payload, q.Payload) {
			t.Fatalf("restore: %+v %v", restored, e)
		}
		r := hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK, Payload: q.Payload}
		encoded, e := WireResult(wire, r)
		if e != nil {
			t.Fatal(e)
		}
		mapped, e := Result(wire, encoded)
		if e != nil || !bytes.Equal(mapped.Payload, q.Payload) {
			t.Fatalf("output changed: %+v %v", mapped, e)
		}
		wire.Depth++
		if _, e = RestoreRequest(wire, q, p.Scope.Incarnation); e == nil {
			t.Fatal("forged wire depth replaced host snapshot")
		}
	}
	q.Kind = hooks.Action
	q.Mode = hooks.Bail
	q.Payload = []byte(`{}`)
	wire, err := Request(q, p.Scope.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"code":-32003,"message":"cancelled"}`, `{"code":-32010,"message":"approval_required"}`, `{"invocation_id":"wrong","status":"cancelled"}`} {
		mapped, e := Result(wire, []byte(raw))
		if e != nil || mapped.Status != hooks.RemoteFailed || mapped.Failure.Code != hooks.InvalidOutput {
			t.Fatalf("non-status veto: %+v %v", mapped, e)
		}
	}
	q.Mode = hooks.Async
	q.Context.BindingID = ""
	if _, err = Notification(hooks.RemoteNotification{Request: q}, p.Scope.Incarnation); err != nil {
		t.Fatal(err)
	}
	q.Context.BindingID = "grant"
	if _, err = Notification(hooks.RemoteNotification{Request: q}, p.Scope.Incarnation); err == nil {
		t.Fatal("notification granted a binding")
	}
	for _, duration := range []time.Duration{time.Nanosecond, 0, -time.Millisecond} {
		q.Context.Timeout = duration
		if _, err = Request(q, p.Scope.Incarnation); err == nil {
			t.Fatalf("nonpositive millisecond lease admitted: %s", duration)
		}
	}
}
func TestLatency(t *testing.T) {
	for _, runtime := range []string{"Go", "Node"} {
		t.Run(runtime, func(t *testing.T) {
			p, err := sdk.DecodeHookHandleParams([]byte(corpus(t)[0].Raw))
			if err != nil {
				t.Fatal(err)
			}
			c, err := startChild(runtime, p.Scope.Incarnation)
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			c.mu.Lock()
			c.queue = 0
			c.mu.Unlock()
			q := hostRequest(p)
			var total, roundtrip time.Duration
			for i := 0; i < 30; i++ {
				start := time.Now()
				wire, err := Request(q, p.Scope.Incarnation)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := sdk.EncodeHookHandleParams(wire)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				began := time.Now()
				reply, err := c.call(ctx, sdk.MethodHookHandle, raw, false)
				roundtrip += time.Since(began)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				result, err := Result(wire, reply)
				if err != nil || result.Status != hooks.RemoteOK {
					t.Fatalf("latency mapping: %+v %v", result, err)
				}
				total += time.Since(start)
			}
			// This harness serializes no competing writer, so queue measurements below
			// mean mutex acquisition only, not production scheduling or pipe backpressure.
			t.Logf("LATENCY %s n=30 warm sequential mean_round_trip=%s mean_end_to_end=%s; writer_queue=%s (mutex only, no contention); startup excluded", runtime, roundtrip/30, total/30, c.queue/30)
		})
	}
}

func TestRealChildPayloadLiterals(t *testing.T) {
	for _, runtime := range []string{"Go", "Node"} {
		t.Run(runtime, func(t *testing.T) {
			p, err := sdk.DecodeHookHandleParams([]byte(corpus(t)[0].Raw))
			if err != nil {
				t.Fatal(err)
			}
			c, err := startChild(runtime, p.Scope.Incarnation)
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			for _, literal := range []string{`null`, "{\n \"n\":1.50,\"large\":9007199254740993,\"x\\u005b\":\"<x>&\"\n}"} {
				q := hostRequest(p)
				q.Payload = []byte(literal)
				wire, err := Request(q, p.Scope.Incarnation)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := sdk.EncodeHookHandleParams(wire)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				reply, err := c.call(ctx, sdk.MethodHookHandle, raw, false)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				result, err := Result(wire, reply)
				if err != nil || result.Status != hooks.RemoteOK {
					t.Fatalf("%+v %v", result, err)
				}
				var expected bytes.Buffer
				if err = json.Compact(&expected, q.Payload); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(result.Payload, expected.Bytes()) {
					t.Fatalf("literal tokens changed: %s", result.Payload)
				}
			}
		})
	}
}

func TestRealChildOperationalModes(t *testing.T) {
	for _, runtime := range []string{"Go", "Node"} {
		t.Run(runtime, func(t *testing.T) {
			p, err := sdk.DecodeHookHandleParams([]byte(corpus(t)[0].Raw))
			if err != nil {
				t.Fatal(err)
			}
			c, err := startChild(runtime, p.Scope.Incarnation)
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			p.Kind = "action"
			p.Mode = "bail"
			for _, v := range corpus(t) {
				if v.DTO != "HookHandleResult" || !v.Valid {
					continue
				}
				r, err := sdk.DecodeHookHandleResult([]byte(v.Raw))
				if err != nil || r.Error == nil {
					continue
				}
				p.Metadata = map[string]string{"fixture": "fail:" + r.Error.Code}
				raw, err := sdk.EncodeHookHandleParams(p)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				reply, err := c.call(ctx, sdk.MethodHookHandle, raw, false)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				mapped, err := Result(p, reply)
				if err != nil || mapped.Status != hooks.RemoteFailed || mapped.Failure.Code != hooks.RemoteFailureCode(r.Error.Code) {
					t.Fatalf("failure mapping: %+v %v", mapped, err)
				}
			}
			for _, test := range []struct{ directive, code string }{{"error", "handler_error"}, {"panic", "handler_panic"}, {"invalid_output", "invalid_output"}, {"wait:1000", "deadline_exceeded"}} {
				p.Context.TimeoutMS = 10
				p.AggregateBudgetMS = 10
				p.Metadata = map[string]string{"fixture": test.directive}
				raw, err := sdk.EncodeHookHandleParams(p)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				reply, err := c.call(ctx, sdk.MethodHookHandle, raw, false)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				mapped, err := Result(p, reply)
				if err != nil || mapped.Status != hooks.RemoteFailed || mapped.Failure.Code != hooks.RemoteFailureCode(test.code) {
					t.Fatalf("%s: %+v %v", test.directive, mapped, err)
				}
			}
			p.Metadata = map[string]string{"fixture": "exit"}
			raw, err := sdk.EncodeHookHandleParams(p)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err = c.call(ctx, sdk.MethodHookHandle, raw, false); err == nil {
				t.Fatal("dead child returned a result")
			}
		})
	}
}
