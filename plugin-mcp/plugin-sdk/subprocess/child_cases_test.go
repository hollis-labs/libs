package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

// Private fixture activation only. These callbacks use the merged author clients;
// the real runtime still declines reverse negotiation in Init.
type childCases struct {
	writer *frameWriter
	transcriptBase
	mu         sync.Mutex
	core       *correlation
	init       InitParams
	event      func(any)
	gates      map[string]chan struct{}
	counts     map[string]int
	profile    string
	armed      bool
	negotiated bool
}

func newChildCases(profile string, core *correlation, event func(any)) *childCases {
	return &childCases{core: core, event: event, gates: map[string]chan struct{}{}, counts: map[string]int{}, profile: profile}
}
func (p *childCases) gate(name string) <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gates[name] == nil {
		p.gates[name] = make(chan struct{})
	}
	return p.gates[name]
}
func (p *childCases) Release(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if name == "arm-writer" {
		p.armed = true
		return
	}
	c := p.gates[name]
	if c == nil {
		c = make(chan struct{})
		p.gates[name] = c
	}
	select {
	case <-c:
	default:
		close(c)
	}
}
func (p *childCases) Write(raw []byte) (int, error) {
	p.mu.Lock()
	armed := p.armed
	p.armed = false
	p.mu.Unlock()
	if armed {
		gate := p.gate("writer")
		p.event(map[string]any{"kind": "writer_waiting", "bytes": len(raw)})
		<-gate
	}
	return os.Stdout.Write(raw)
}
func (p *childCases) Options() ServeOptions {
	v := ServeOptions{Output: p}
	if p.profile == "expanded-queue" {
		v.QueueLimits = QueueLimits{Frames: 32, Bytes: 32768}
	}
	if p.profile == "expanded-queue-frames" {
		v.QueueLimits = QueueLimits{Frames: 3, Bytes: DefaultQueuedWriteBytes}
	}
	if p.profile == "expanded-overflow" {
		v.FrameLimits.OutputBytes = 1024
	}
	if p.profile == "expanded-hung" || p.profile == "expanded-cleanup-hung" {
		v.ShutdownTimeout = 200 * time.Millisecond
	}
	return v
}
func (p *childCases) Snapshot() map[string]any {
	p.mu.Lock()
	out := map[string]any{}
	for k, v := range p.counts {
		out[k] = v
	}
	w := p.writer
	core := p.core
	p.mu.Unlock()
	if core != nil {
		core.mu.Lock()
		out["reverse_pending"] = len(core.pending)
		core.mu.Unlock()
	}
	if w != nil {
		w.mu.Lock()
		out["ordinary_queued"] = len(w.ordinary.queue)
		out["control_queued"] = len(w.control.queue)
		out["reserved_frames"] = w.control.reservedFrames
		out["reserved_bytes"] = w.control.reservedBytes
		w.mu.Unlock()
	}
	return out
}
func (p *childCases) bump(name string) { p.mu.Lock(); p.counts[name]++; p.mu.Unlock() }
func childID(ctx context.Context) any {
	raw, _ := json.Marshal(scopeFromContext(ctx).id)
	var id any
	_ = json.Unmarshal(raw, &id)
	return id
}
func (p *childCases) entered(ctx context.Context, name string) (any, <-chan struct{}) {
	id := childID(ctx)
	gate := p.gate(fmt.Sprint("request-", id))
	context.AfterFunc(ctx, func() {
		var d *DeadlineExceededError
		var c *TransportCancelledError
		if errors.As(context.Cause(ctx), &d) || errors.As(context.Cause(ctx), &c) {
			p.event(map[string]any{"kind": "aborted", "id": id, "deadline": errors.As(context.Cause(ctx), &d)})
		}
	})
	p.bump("entered")
	p.bump(name)
	_, deadline := ctx.Deadline()
	p.event(map[string]any{"kind": "entered", "id": id, "name": name, "deadline": deadline})
	return id, gate
}
func (p *childCases) Init(ctx context.Context, v InitParams) (InitResult, error) {
	p.mu.Lock()
	p.writer = scopeFromContext(ctx).manager.writer
	p.init = v
	if p.negotiated {
		p.core = scopeFromContext(ctx).manager.core // Observe runtime-owned state, never create or activate it.
	} else {
		p.core.methodTimeoutMS = v.HostServices.Limits.MethodTimeoutMS
	}
	p.mu.Unlock()
	return p.transcriptBase.Init(ctx, v)
}
func (p *childCases) Load(ctx context.Context) (LoadResult, error) {
	id, g := p.entered(ctx, "load")
	<-g
	p.event(map[string]any{"kind": "returned", "id": id})
	return LoadResult{}, nil
}
func (p *childCases) Unload(context.Context) error {
	p.bump("unload_attempts")
	p.event(map[string]any{"kind": "unload_started"})
	switch p.profile {
	case "expanded-cleanup-error":
		return errors.New("fixture cleanup failure")
	case "expanded-cleanup-panic":
		panic("fixture cleanup panic")
	case "expanded-cleanup-hung":
		select {}
	}
	return nil
}
func (p *childCases) Health(context.Context) (HealthStatus, error) {
	p.bump("health")
	return HealthStatus{OK: true}, nil
}
func childFailure(err error) map[string]any {
	if err == nil {
		return map[string]any{"code": "ok", "effect_state": "committed"}
	}
	var host *HostRPCError
	var local *capability.Error
	var transport *RPCTransportError
	if errors.As(err, &host) {
		result := map[string]any{"code": host.Data.Code, "effect_state": host.Data.EffectState}
		if host.Data.Detail != "" {
			result["detail"] = host.Data.Detail
		}
		return result
	}
	if errors.As(err, &local) {
		return map[string]any{"code": local.Code, "effect_state": local.EffectState}
	}
	if errors.As(err, &transport) {
		return map[string]any{"code": transport.Failure.Code, "effect_state": transport.Failure.EffectState}
	}
	return map[string]any{"code": "fixture_failure", "effect_state": "unknown"}
}
func (p *childCases) Command(ctx context.Context, v CommandRequest) (CommandResult, error) {
	id, g := p.entered(ctx, v.Name)
	defer func() { p.bump("returned"); p.event(map[string]any{"kind": "returned", "id": id}) }()
	switch v.Name {
	case "hold", "uncooperative":
		<-g
	case "cooperative":
		select {
		case <-g:
		case <-ctx.Done():
		}
	case "hung":
		select {}
	case "overflow":
		p.bump("commits")
		return CommandResult{Action: "message", Content: strings.Repeat("x", 2048)}, nil
	case "get", "put":
		var a struct {
			N            int    `json:"n"`
			Key          string `json:"key"`
			OperationKey string `json:"operation_key"`
			ValueBytes   int    `json:"value_bytes"`
			Gate         bool   `json:"gate"`
			ReturnGate   bool   `json:"return_gate"`
		}
		if json.Unmarshal([]byte(v.Args), &a) != nil || a.N < 1 || a.N > 9 || a.ValueBytes < 0 || a.ValueBytes > 65536 {
			return CommandResult{}, errors.New("fixture arguments")
		}
		if a.Gate {
			<-g
		}
		hctx := ctx
		if !p.negotiated {
			var err error
			hctx, err = hostClientContext(ctx, p.core, p.init, newSecretTracker(), time.Time{})
			if err != nil {
				return CommandResult{}, err
			}
		}
		h, ok := HostClientFromContext(hctx)
		if !ok {
			return CommandResult{}, errors.New("missing delivered host client")
		}
		var wg sync.WaitGroup
		results := make([]map[string]any, a.N)
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				var err error
				if v.Name == "get" {
					_, err = h.StorageGet(hctx, StorageGetArgs{"g-StorageGet", a.Key})
				} else {
					value, _ := json.Marshal(strings.Repeat("v", a.ValueBytes))
					_, err = h.StoragePut(hctx, StoragePutArgs{GrantID: "g-StoragePut", Key: a.Key, Value: value, OperationKey: a.OperationKey})
				}
				results[i] = childFailure(err)
				p.event(map[string]any{"kind": "helper_done", "id": id, "index": i, "failure": results[i]})
			}(i)
		}
		wg.Wait()
		if a.ReturnGate {
			<-g
		}
		raw, _ := json.Marshal(results)
		return CommandResult{Action: "message", Content: string(raw)}, nil
	}
	return CommandResult{Action: "noop"}, nil
}
