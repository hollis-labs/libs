package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// A dedicated normal-Serve child. It never constructs correlation, clients or grants.
func TestNegotiatedFixtureChild(t *testing.T) {
	profile := os.Getenv("SDK_FIXTURE_CHILD")
	if !strings.HasPrefix(profile, "negotiated:") {
		return
	}
	profile = strings.TrimPrefix(profile, "negotiated:")
	event := func(v any) {
		raw, _ := json.Marshal(map[string]any{"fixture_event": v})
		os.Stderr.Write(append(raw, '\n'))
	}
	cases := newChildCases(profile, nil, event)
	cases.negotiated = true
	p := &negotiatedChild{childCases: cases, mode: profile}
	input := os.NewFile(3, "fixture-control")
	go func() {
		reader := bufio.NewReaderSize(input, 4096)
		last := 0
		for {
			raw, err := reader.ReadSlice('\n')
			if err != nil {
				return
			}
			var v struct {
				Seq      int
				Op, Gate string
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if len(raw) > 4096 || strictjson.ValidatePortable(raw) != nil || decoder.Decode(&v) != nil || v.Seq != last+1 || v.Op != "release" || len(v.Gate) > 128 {
				os.Exit(1)
			}
			last = v.Seq
			p.Release(v.Gate)
			if v.Gate == "probe-cached" {
				p.probe()
			}
			if v.Gate == "snapshot" {
				event(map[string]any{"kind": "snapshot", "effects": p.Snapshot()})
			}
			event(map[string]any{"kind": "control_received", "seq": last})
		}
	}()
	event(map[string]any{"kind": "ready"})
	opt := p.Options()
	opt.ReverseRPC = profile != "no-opt-in" && profile != "authored-only"
	if profile == "local-limits" {
		opt.FrameLimits = FrameLimits{InputBytes: 4096, OutputBytes: 4096}
		opt.QueueLimits = QueueLimits{Frames: 32, Bytes: 8192}
		opt.WriteTimeout = 300 * time.Millisecond
	}
	err := ServeWithOptions(p, opt)
	var transport any
	if err != nil {
		transport = fmt.Sprintf("%T", err)
	}
	event(map[string]any{"kind": "finished", "effects": p.Snapshot(), "transport_error": transport})
	input.Close()
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type negotiatedChild struct {
	*childCases
	mode      string
	cacheMu   sync.Mutex
	cached    *HostClient
	cachedCtx context.Context
}

func (p *negotiatedChild) observe(ctx context.Context, kind string) *HostClient {
	h, ok := HostClientFromContext(ctx)
	var id any
	if scopeFromContext(ctx) != nil {
		id = childID(ctx)
	}
	p.event(map[string]any{"kind": kind, "client": ok, "id": id})
	if ok {
		p.cacheMu.Lock()
		p.cached = h
		p.cachedCtx = ctx
		p.cacheMu.Unlock()
	}
	return h
}
func (p *negotiatedChild) probe() {
	p.cacheMu.Lock()
	h, ctx := p.cached, p.cachedCtx
	p.cacheMu.Unlock()
	var err error = errors.New("no cached client")
	if h != nil {
		_, err = h.Log(ctx, HostLogArgs{GrantID: "g-Log", Level: "info", Message: "cached"})
	}
	p.event(map[string]any{"kind": "cached_probe", "failure": childFailure(err)})
}
func (p *negotiatedChild) log(ctx context.Context) error {
	h := p.observe(ctx, "lifecycle_client")
	if h == nil {
		return nil
	}
	_, err := h.StorageGet(ctx, StorageGetArgs{GrantID: "g-StorageGet", Key: "denied"})
	p.event(map[string]any{"kind": "lifecycle_business", "failure": childFailure(err)})
	_, err = h.Log(ctx, HostLogArgs{GrantID: "g-Log", Level: "info", Message: "lifecycle"})
	return err
}
func (p *negotiatedChild) Init(ctx context.Context, v InitParams) (InitResult, error) {
	r, err := p.childCases.Init(ctx, v)
	h := p.observe(ctx, "init_client")
	if p.mode == "policy" || p.mode == "local-limits" {
		a := scopeFromContext(ctx).manager
		a.mu.Lock()
		l := a.limits
		a.mu.Unlock()
		w := a.writer
		w.mu.Lock()
		frame, queue, timeout := w.frameLimit, w.limits.Bytes, w.timeout
		w.mu.Unlock()
		p.event(map[string]any{"kind": "policy", "forward": l.Forward, "reverse": l.Reverse, "control": l.Control, "frame": frame, "queue": queue, "write_ms": timeout.Milliseconds()})
	}
	if p.mode == "mutate" {
		v.HostServices.Methods = nil
		v.HostServices.Limits.MethodTimeoutMS = nil
		v.Grants = nil
	}
	if p.mode == "authored-only" || p.mode == "authored-ack" || p.mode == "observe-authored" {
		one := 1
		r.ReverseRPCVersion = &one
	}
	if p.mode == "fallback" {
		r.Description = strings.Repeat("x", 4096)
	}
	if p.mode == "init-error" {
		return InitResult{}, errors.New("fixture failed Init")
	}
	if p.mode == "init-panic" {
		panic("fixture Init panic")
	}
	if p.mode == "lifecycle" {
		err = p.log(ctx)
	}
	if p.mode == "pending-init" {
		if h == nil {
			return InitResult{}, errors.New("no provisional client")
		}
		go func() {
			_, e := h.Log(ctx, HostLogArgs{GrantID: "g-Log", Level: "info", Message: "pending"})
			p.event(map[string]any{"kind": "pending_done", "failure": childFailure(e)})
		}()
		<-p.gate("fail-init")
		return InitResult{}, errors.New("fixture failed Init")
	}
	if p.mode == "wait-init" {
		<-ctx.Done()
	}
	return r, err
}
func (p *negotiatedChild) Health(ctx context.Context) (HealthStatus, error) {
	if strings.HasPrefix(p.mode, "expanded") {
		return p.childCases.Health(ctx)
	}
	h := p.observe(ctx, "health_client")
	if p.mode == "get" || p.mode == "mutate" || p.mode == "authored-ack" || p.mode == "subset" {
		if h == nil {
			return HealthStatus{OK: false}, nil
		}
		_, err := h.StorageGet(ctx, StorageGetArgs{GrantID: "g-StorageGet", Key: "read"})
		p.event(map[string]any{"kind": "health_helper", "failure": childFailure(err)})
		return HealthStatus{OK: err == nil}, nil
	}
	return HealthStatus{OK: true}, nil
}
func (p *negotiatedChild) Load(ctx context.Context) (LoadResult, error) {
	if p.mode == "lifecycle" {
		return LoadResult{}, p.log(ctx)
	}
	return p.childCases.Load(ctx)
}
func (p *negotiatedChild) Unload(ctx context.Context) error {
	if !strings.HasPrefix(p.mode, "expanded") {
		p.bump("unload_attempts")
		p.event(map[string]any{"kind": "unload_started"})
		if p.mode == "lifecycle" {
			return p.log(ctx)
		}
		p.observe(ctx, "cleanup_client")
		return nil
	}
	return p.childCases.Unload(ctx)
}

func (p *negotiatedChild) HookHandle(ctx context.Context, v HookHandleParams) (HookHandleResult, error) {
	p.observe(ctx, "hook_client")
	r := HookHandleResult{InvocationID: v.InvocationID, Status: "ok"}
	if v.Kind == "filter" {
		r.Payload = v.Payload
	}
	return r, nil
}
