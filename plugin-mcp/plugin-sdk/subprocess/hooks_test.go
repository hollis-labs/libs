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

func (p *hookTranscriptPlugin) Init(ctx context.Context, params InitParams) (InitResult, error) {
	r, err := p.transcriptFull.Init(ctx, params)
	v := 1
	r.HooksProfileVersion = &v
	return r, err
}
func (p *hookTranscriptPlugin) HookHandle(ctx context.Context, r HookHandleParams) (HookHandleResult, error) {
	switch r.Metadata["fixture"] {
	case "cancelled", "approval_required":
		reason := "fixture veto"
		return HookHandleResult{InvocationID: r.InvocationID, Status: r.Metadata["fixture"], Reason: &reason}, nil
	case "handler_error":
		return HookHandleResult{}, errors.New("private backend")
	case "panic":
		panic("fixture panic")
	case "invalid_output":
		return HookHandleResult{InvocationID: "wrong", Status: "ok"}, nil
	case "wait":
		<-ctx.Done()
		return HookHandleResult{}, ctx.Err()
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

// Child enablement exists only in the test binary, never an installed plugin.
func TestHookChild(t *testing.T) {
	profile := os.Getenv("HOOK_TEST_CHILD")
	if profile == "" {
		return
	}
	if profile == "hooks-fixture" {
		hookFixtureSetup = func(s *server) { s.hooksFixtureEnabled = true }
	}
	if err := Serve(&hookTranscriptPlugin{}); err != nil {
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
