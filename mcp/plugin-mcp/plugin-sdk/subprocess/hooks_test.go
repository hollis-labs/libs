package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func enableHookFixture(t *testing.T) {
	t.Helper()
	old := hookFixtureSetup
	hookFixtureSetup = func(s *server) { s.hooksFixtureEnabled = true }
	t.Cleanup(func() { hookFixtureSetup = old })
}

type hookTranscriptPlugin struct{ transcriptFull }

// This plugin authors profile fields without implementing HookHandler. The
// runtime must not let authored acknowledgements enable a connection.
type hookDeclinedPlugin struct{ transcriptBase }

func (p *hookDeclinedPlugin) Init(ctx context.Context, params InitParams) (InitResult, error) {
	r, err := p.transcriptBase.Init(ctx, params)
	v := 1
	r.HooksProfileVersion = &v
	r.ReverseRPCVersion = &v
	return r, err
}

func hookTranscriptFor(profile string) Plugin {
	if profile == "hooks-declined" {
		return &hookDeclinedPlugin{}
	}
	return &hookTranscriptPlugin{}
}

func (p *hookTranscriptPlugin) HookHandle(ctx context.Context, r HookHandleParams) (HookHandleResult, error) {
	directive := r.Metadata["fixture"]
	switch {
	case directive == "exit":
		os.Exit(23)
	case directive == "script":
		var script struct {
			DelayMS uint32 `json:"delay_ms"`
		}
		raw := []byte(r.Metadata["script"])
		if json.Unmarshal(raw, &script) != nil {
			return HookHandleResult{InvocationID: r.InvocationID, Status: "invalid"}, nil
		}
		if script.DelayMS > 0 {
			if err := hookFixtureWait(ctx, time.Duration(script.DelayMS)*time.Millisecond); err != nil {
				return HookHandleResult{}, err
			}
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return HookHandleResult{InvocationID: r.InvocationID, Status: "invalid"}, nil
		}
		delete(fields, "delay_ms")
		fields["invocation_id"], _ = json.Marshal(r.InvocationID)
		var members [][]byte
		for key, value := range fields {
			name, _ := json.Marshal(key)
			members = append(members, append(append(name, ':'), value...))
		}
		raw = append(append([]byte("{"), bytes.Join(members, []byte(","))...), '}')
		var result HookHandleResult
		if json.Unmarshal(raw, &result) != nil {
			return HookHandleResult{InvocationID: r.InvocationID, Status: "invalid"}, nil
		}
		return result, nil
	case strings.HasPrefix(directive, "fail:"):
		return HookHandleResult{InvocationID: r.InvocationID, Status: "failed", Error: &HookFailure{Code: strings.TrimPrefix(directive, "fail:")}}, nil
	case directive == "veto" || directive == "cancelled" || directive == "approval_required":
		status := directive
		if status == "veto" {
			status = "cancelled"
		}
		reason := "fixture veto"
		return HookHandleResult{InvocationID: r.InvocationID, Status: status, Reason: &reason}, nil
	case directive == "error" || directive == "handler_error":
		return HookHandleResult{}, errors.New("private backend")
	case directive == "panic" || directive == "throw":
		panic("fixture panic")
	case directive == "invalid_output":
		return HookHandleResult{InvocationID: "wrong", Status: "ok"}, nil
	case directive == "wait":
		<-ctx.Done()
		return HookHandleResult{}, ctx.Err()
	case strings.HasPrefix(directive, "wait:") || directive == "slow-notification":
		delay := 100 * time.Millisecond
		if directive != "slow-notification" {
			ms, err := strconv.ParseUint(strings.TrimPrefix(directive, "wait:"), 10, 32)
			if err != nil {
				return HookHandleResult{}, errors.New("invalid fixture delay")
			}
			delay = time.Duration(ms) * time.Millisecond
		}
		if err := hookFixtureWait(ctx, delay); err != nil {
			return HookHandleResult{}, err
		}
	}

	result := HookHandleResult{InvocationID: r.InvocationID, Status: "ok"}
	if r.Kind == "filter" {
		result.Payload = append(json.RawMessage(nil), r.Payload...)
	}
	return result, nil
}

type hookVector struct {
	Name         string            `json:"name"`
	DTO          string            `json:"dto"`
	Raw          string            `json:"raw"`
	Valid        bool              `json:"valid"`
	Notification *bool             `json:"notification"`
	Request      *HookHandleParams `json:"request"`
}

func TestHookDTOFixtures(t *testing.T) {
	b, err := os.ReadFile("../protocol/v2/fixtures/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []hookVector
	if err := json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			err := ValidateHookDTO(v.DTO, []byte(v.Raw))
			if err == nil && v.Notification != nil {
				if v.DTO == "HookHandleParams" {
					var p HookHandleParams
					err = json.Unmarshal([]byte(v.Raw), &p)
					if err == nil {
						err = ValidateHookRequest(p, *v.Notification)
					}
				} else {
					var p HookHandleBatchParams
					err = json.Unmarshal([]byte(v.Raw), &p)
					if err == nil {
						err = ValidateHookBatchRequest(p, *v.Notification)
					}
				}
			}
			if err == nil && v.Request != nil {
				var r HookHandleResult
				err = json.Unmarshal([]byte(v.Raw), &r)
				if err == nil {
					err = ValidateHookResultFor(*v.Request, r)
				}
			}
			if (err == nil) != v.Valid {
				t.Fatalf("valid=%v err=%v", v.Valid, err)
			}
		})
	}
}
func hookTestParams(t *testing.T) HookHandleParams {
	t.Helper()
	b, err := os.ReadFile("../protocol/v2/fixtures/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	var v []hookVector
	_ = json.Unmarshal(b, &v)
	var p HookHandleParams
	if err := json.Unmarshal([]byte(v[0].Raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestHookLiteralAndScope(t *testing.T) {
	p := hookTestParams(t)
	p.Payload = json.RawMessage(`{ "n" : 1.50, "large":9007199254740993,"x\u005b":"<x>&"}`)
	raw, err := p.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, p.Payload) {
		t.Fatalf("literal lost: %s", raw)
	}
	r := hookInvoke(context.Background(), &hookTranscriptPlugin{}, p)
	if !bytes.Equal(p.Payload, r.Payload) {
		t.Fatal("handler lost literal")
	}
	raw, err = r.MarshalJSON()
	if err != nil || !bytes.Contains(raw, p.Payload) {
		t.Fatalf("result literal lost %s %v", raw, err)
	}
	p.Payload = nil
	if p.Validate() == nil {
		t.Fatal("missing payload accepted")
	}
	for _, code := range []int{-32003, -32010} {
		if _, err := HookRPCError(code, "invalid_params", ""); err == nil {
			t.Fatal("application error reclassified")
		}
	}
}
func TestHookLeaseIncludesBatchQueueAndCancellation(t *testing.T) {
	p := hookTestParams(t)
	p.Kind = "action"
	p.Mode = "sequential"
	p.Context.TimeoutMS = 10
	p.AggregateBudgetMS = 5
	p.Metadata = map[string]string{"fixture": "wait"}
	ctx, cancel := hookLease(context.Background(), p, time.Now())
	defer cancel()
	r := hookAwait(ctx, &hookTranscriptPlugin{}, p)
	if r.Status != "failed" || r.Error.Code != "deadline_exceeded" {
		t.Fatalf("timeout %+v", r)
	}
	parent, stop := context.WithCancel(context.Background())
	stop()
	ctx, close := hookLease(parent, p, time.Now())
	defer close()
	r = hookAwait(ctx, &hookTranscriptPlugin{}, p)
	if r.Error.Code != "caller_cancelled" {
		t.Fatalf("cancel %+v", r)
	}
}

// Only the legacy fixture profile bypasses negotiation in this test binary.
func TestHookChild(t *testing.T) {
	profile := os.Getenv("HOOK_TEST_CHILD")
	if profile == "" {
		return
	}
	if profile == "hooks-fixture" {
		hookFixtureSetup = func(s *server) { s.hooksFixtureEnabled = true }
	}
	// The programmable raw lane is a NON-SDK fake-reply test double. The
	// request still enters Serve, but the output wrapper replaces only named replies.
	raw := &hookFixtureRaw{replies: map[string]json.RawMessage{}}
	if err := ServeWithOptions(hookTranscriptFor(profile), ServeOptions{Input: raw.input(os.Stdin), Output: raw}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
func TestHookRealChildTranscripts(t *testing.T) {
	paths, err := filepath.Glob("../docs/protocol/v2/transcripts/hooks-*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fixture transcript
			if err := json.Unmarshal(b, &fixture); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestHookChild$")
			cmd.Env = append(os.Environ(), "HOOK_TEST_CHILD="+fixture.Profile)
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			reader := bufio.NewReader(out)
			for i, step := range fixture.Steps {
				frame := step.Raw
				if frame == "" {
					frame = string(step.Send)
				}
				var compact bytes.Buffer
				if step.Raw == "" {
					if err := json.Compact(&compact, step.Send); err != nil {
						t.Fatal(err)
					}
					frame = compact.String()
				}
				if _, err := io.WriteString(in, frame+"\n"); err != nil {
					t.Fatal(err)
				}
				if len(step.Expect) == 0 {
					continue
				}
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Fatalf("step %d read: %v %s", i, err, stderr.String())
				}
				compareTranscriptResponse(t, i, line, step)
			}
			_ = in.Close()
			extra, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(extra)) != "" {
				t.Fatalf("unexpected child reply %s", extra)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("child %v %s", err, stderr.String())
			}
		})
	}
}

type hookCounterPlugin struct {
	hookTranscriptPlugin
	calls int
}

func (p *hookCounterPlugin) HookHandle(ctx context.Context, r HookHandleParams) (HookHandleResult, error) {
	p.calls++
	return p.hookTranscriptPlugin.HookHandle(ctx, r)
}
func TestHookBatchAtomicAdmissionAndExpiredQueue(t *testing.T) {
	p := hookTestParams(t)
	p.Kind = "action"
	p.Mode = "parallel"
	p.Payload = json.RawMessage("null")
	second := p
	second.InvocationID = "second"
	second.Context.BindingID = nil
	plugin := &hookCounterPlugin{}
	var frames [][]byte
	s := &server{plugin: plugin, hooksFixtureEnabled: true, hooksIncarnation: p.Scope.Incarnation, logger: newStderrLogger(newSecretTracker()), writeFrame: func(b []byte) { frames = append(frames, b) }}
	id := NumberID(7)
	s.dispatchHook(context.Background(), RPCRequest{ID: id, Method: MethodHookHandleBatch, Params: HookHandleBatchParams{Items: []HookHandleParams{p, second}}})
	if plugin.calls != 0 || len(frames) != 1 {
		t.Fatalf("partial admission calls=%d frames=%d", plugin.calls, len(frames))
	}
	var response RPCResponse
	if err := json.Unmarshal(frames[0], &response); err != nil || response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("response %s %v", frames[0], err)
	}
	// Lease already spent in decode/queue must never run user code.
	p.Context.TimeoutMS = 1
	ctx, cancel := hookLease(context.Background(), p, time.Now().Add(-time.Second))
	defer cancel()
	r := hookAwait(ctx, plugin, p)
	if plugin.calls != 0 || r.Error.Code != "deadline_exceeded" {
		t.Fatalf("expired queue %+v calls=%d", r, plugin.calls)
	}
}

func TestHookRepliesRespectFrameLimits(t *testing.T) {
	p := hookTestParams(t)
	p.Payload = json.RawMessage(`"` + strings.Repeat("x", 1024) + `"`)
	for _, limit := range []int{512, 32} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			var frames [][]byte
			var fenced error
			s := &server{plugin: &hookTranscriptPlugin{}, hooksFixtureEnabled: true, hooksIncarnation: p.Scope.Incarnation, logger: newStderrLogger(newSecretTracker()), outputLimit: limit, fence: func(err error) { fenced = err }, writeFrame: func(b []byte) { frames = append(frames, b) }}
			s.dispatchHook(context.Background(), RPCRequest{ID: NumberID(7), Method: MethodHookHandle, Params: p})
			if limit == 32 {
				if fenced == nil || len(frames) != 0 {
					t.Fatalf("unbounded fallback frames=%d fenced=%v", len(frames), fenced)
				}
				return
			}
			if fenced != nil || len(frames) != 1 || len(frames[0]) > limit {
				t.Fatalf("frames=%d fenced=%v", len(frames), fenced)
			}
			var r RPCResponse
			if json.Unmarshal(frames[0], &r) != nil || r.Error == nil || r.Error.Code != -32603 || len(r.Result) != 0 {
				t.Fatalf("partial success %s", frames[0])
			}
		})
	}
}

func hookFixtureWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// hookFixtureRaw is confined to the SDK TEST binary. raw:<name> deliberately
// bypasses SDK result validation and is never evidence of SDK author behaviour.
type hookFixtureRaw struct {
	mu      sync.Mutex
	replies map[string]json.RawMessage
}

func (f *hookFixtureRaw) input(in io.Reader) io.Reader {
	reader, writer := io.Pipe()
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), MaxHookDTOBytes+4096)
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					InvocationID string            `json:"invocation_id"`
					Metadata     map[string]string `json:"metadata"`
				} `json:"params"`
			}
			if json.Unmarshal(line, &request) == nil && request.Method == MethodHookHandle && len(request.ID) > 0 {
				directive := request.Params.Metadata["fixture"]
				if strings.HasPrefix(directive, "raw:") {
					if result := hookFixtureRawResult(strings.TrimPrefix(directive, "raw:"), request.Params.InvocationID); result != nil {
						f.mu.Lock()
						f.replies[string(request.ID)] = result
						f.mu.Unlock()
					}
				}
			}
			if _, err := writer.Write(append(line, '\n')); err != nil {
				return
			}
		}
		_ = writer.CloseWithError(scanner.Err())
	}()
	return reader
}
func (f *hookFixtureRaw) Write(line []byte) (int, error) {
	inputLength := len(line)
	var response struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(line, &response) == nil {
		f.mu.Lock()
		replacement, ok := f.replies[string(response.ID)]
		delete(f.replies, string(response.ID))
		f.mu.Unlock()
		if ok {
			line = append(append(append([]byte(`{"jsonrpc":"2.0","id":`), response.ID...), []byte(`,"result":`)...), replacement...)
			line = append(line, '}', '\n')
		}
	}
	_, err := os.Stdout.Write(line)
	// Writers must report the size of the SDK frame, not the injected frame.
	if err != nil {
		return 0, err
	}
	return inputLength, nil
}
func hookFixtureRawResult(name, id string) json.RawMessage {
	quoted, _ := json.Marshal(id)
	prefix := `{"invocation_id":` + string(quoted)
	switch name {
	case "unknown-status":
		return json.RawMessage(prefix + `,"status":"success"}`)
	case "unknown-code":
		return json.RawMessage(prefix + `,"status":"failed","error":{"code":"cancelled"}}`)
	case "action-output":
		return json.RawMessage(prefix + `,"status":"ok","payload":null}`)
	case "non-ok-output":
		return json.RawMessage(prefix + `,"status":"failed","payload":{},"error":{"code":"handler_error"}}`)
	case "wrong-id":
		return json.RawMessage(`{"invocation_id":"wrong","status":"ok"}`)
	}
	return nil
}

func TestHookBridgeFixtureDirectives(t *testing.T) {
	plugin := &hookTranscriptPlugin{}
	for _, code := range []string{"remote_not_allowed", "latency_budget_exceeded", "stale_scope", "stale_binding", "capacity_exhausted", "deadline_exceeded", "caller_cancelled", "depth_exceeded", "callback_cycle", "transport_failure", "handler_panic", "invalid_output", "handler_error", "schema_mismatch", "profile_unavailable"} {
		t.Run(code, func(t *testing.T) {
			p := hookTestParams(t)
			p.Metadata = map[string]string{"fixture": "fail:" + code}
			r := hookInvoke(context.Background(), plugin, p)
			if r.Error == nil || r.Error.Code != code {
				t.Fatalf("%+v", r)
			}
		})
	}
	p := hookTestParams(t)
	literal := `{"n":1.50,"large":9007199254740993,"x\u005b":"<x>&"}`
	p.Metadata = map[string]string{"fixture": "script", "script": `{"status":"ok","payload":` + literal + `}`}
	r := hookInvoke(context.Background(), plugin, p)
	if string(r.Payload) != literal {
		t.Fatalf("script literal lost: %s", r.Payload)
	}
	p.Metadata["script"] = `{"status":"ok","payload":{},"unknown":true}`
	if r = hookInvoke(context.Background(), plugin, p); r.Error == nil || r.Error.Code != "invalid_output" {
		t.Fatalf("invalid script accepted: %+v", r)
	}
	for _, directive := range []string{"wait:1000", "slow-notification"} {
		p.Metadata = map[string]string{"fixture": directive}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := plugin.HookHandle(ctx, p); !errors.Is(err, context.Canceled) {
			t.Fatalf("wait ignored cancellation: %v", err)
		}
	}
	for _, name := range []string{"unknown-status", "unknown-code", "action-output", "non-ok-output", "wrong-id"} {
		raw := hookFixtureRawResult(name, p.InvocationID)
		result, err := DecodeHookHandleResult(raw)
		p.Kind = "action"
		p.Mode = "bail"
		if err == nil {
			err = ValidateHookResultFor(p, result)
		}
		if err == nil {
			t.Fatalf("raw fake reply %s unexpectedly valid", name)
		}
	}
}

// Reusing one author object across connections must not carry negotiation state.
func TestHookNegotiationIsConnectionLocal(t *testing.T) {
	p := &hookTranscriptPlugin{}
	hook := hookTestParams(t)
	for _, offered := range []bool{true, false} {
		var frames [][]byte
		s := &server{plugin: p, logger: newStderrLogger(newSecretTracker()), writeFrame: func(b []byte) { frames = append(frames, b) }}
		params := validInitParams()
		params.Incarnation = hook.Scope.Incarnation
		if offered {
			params.HooksProfile = &HooksProfile{HooksProfileVersion: 1}
		}
		s.dispatch(context.Background(), RPCRequest{JSONRPC: "2.0", ID: NumberID(1), Method: MethodInit, Params: params})
		var init RPCResponse
		if err := json.Unmarshal(frames[0], &init); err != nil {
			t.Fatal(err)
		}
		if init.Error != nil {
			t.Fatal(init.Error)
		}
		var result InitResult
		if err := json.Unmarshal(init.Result, &result); err != nil {
			t.Fatal(err)
		}
		if offered {
			if result.HooksProfileVersion == nil || *result.HooksProfileVersion != 1 {
				t.Fatalf("missing ack: %+v", result)
			}
		} else if result.HooksProfileVersion != nil {
			t.Fatal("negotiation leaked across connections")
		}
		s.dispatch(context.Background(), RPCRequest{JSONRPC: "2.0", ID: NumberID(2), Method: MethodHookHandle, Params: hook})
		var reply RPCResponse
		if err := json.Unmarshal(frames[1], &reply); err != nil {
			t.Fatal(err)
		}
		if offered {
			if reply.Error != nil {
				t.Fatal(reply.Error)
			}
			var r HookHandleResult
			if err := json.Unmarshal(reply.Result, &r); err != nil {
				t.Fatal(err)
			}
			if r.Status != "ok" {
				t.Fatalf("unexpected hook result: %+v", r)
			}
		} else if reply.Error == nil || reply.Error.Code != -32601 {
			t.Fatalf("hook allowed without offer: %+v", reply)
		}
	}
}
