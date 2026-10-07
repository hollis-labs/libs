package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
)

// This entry runs only in a dedicated test binary launched by the Node parent.
// Normal Go tests neither spawn Node nor depend on npm artifacts.
func TestDuplexFixtureChild(t *testing.T) {
	profile := os.Getenv("SDK_FIXTURE_CHILD")
	if profile == "" {
		return
	}
	var unloads atomic.Int32
	event := func(v any) {
		b, _ := json.Marshal(map[string]any{"fixture_event": v})
		_, _ = os.Stderr.Write(append(b, '\n'))
	}
	if profile == "control-only" {
		event(map[string]any{"kind": "ready"})
		input := os.NewFile(3, "fixture-control")
		reader := bufio.NewReaderSize(input, 4096)
		raw, err := reader.ReadSlice('\n')
		if err != nil || len(raw) > 4096 {
			os.Exit(1)
		}
		var value struct {
			Seq  int    `json:"seq"`
			Op   string `json:"op"`
			Gate string `json:"gate"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Seq != 1 || value.Op != "release" || value.Gate != "feasibility" {
			os.Exit(1)
		}
		event(map[string]any{"kind": "control_received", "seq": value.Seq})
		os.Exit(0)
	}
	var p Plugin
	switch profile {
	case "base":
		p = &childBase{count: &unloads}
	case "full":
		p = &childFull{count: &unloads, event: event}
	case "lifecycle-shutdown":
		p = &childShutdown{count: &unloads}
	case "lifecycle-error":
		p = &childLifecycleError{count: &unloads}
	case "health-error":
		p = &childHealthError{count: &unloads}
	case "frame-output":
		p = &childFrameOutput{count: &unloads}
	case "duplex-smoke":
	default:
		if !strings.HasPrefix(profile, "expanded") {
			os.Exit(2)
		}

	}
	core := newFixtureCorrelation(profile == "duplex-smoke" || strings.HasPrefix(profile, "expanded") && profile != "expanded-base")
	if strings.HasPrefix(profile, "expanded") {
		p = newChildCases(profile, core, event)
	}
	if profile == "duplex-smoke" {
		p = &childDuplex{core: core, count: &unloads}
	}
	control := os.NewFile(3, "fixture-control")
	var stopped atomic.Bool
	go func() {
		reader := bufio.NewReaderSize(control, 4096)
		last := 0
		for {
			raw, err := reader.ReadSlice('\n')
			if err != nil {
				if !stopped.Load() {
					event(map[string]any{"kind": "control_failure"})
					os.Exit(1)
				}
				return
			}
			var value struct {
				Seq  int    `json:"seq"`
				Op   string `json:"op"`
				Gate string `json:"gate"`
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if len(raw) > 4096 || strictjson.ValidatePortable(raw) != nil || decoder.Decode(&value) != nil || value.Seq != last+1 || value.Op != "release" || len(value.Gate) > 128 {
				event(map[string]any{"kind": "control_failure"})
				os.Exit(1)
			}
			last = value.Seq
			if provider, ok := p.(interface{ Release(string) }); ok {
				provider.Release(value.Gate)
			}
			event(map[string]any{"kind": "control_received", "seq": last})
			if value.Gate == "snapshot" {
				if provider, ok := p.(interface{ Snapshot() map[string]any }); ok {
					event(map[string]any{"kind": "snapshot", "effects": provider.Snapshot()})
				}
			}
		}
	}()
	event(map[string]any{"kind": "ready"})
	// Omitted injected streams keep runtime-owned stdin/stdout and actual signals.
	options := ServeOptions{}
	if provider, ok := p.(interface{ Options() ServeOptions }); ok {
		options = provider.Options()
	}
	err := serveConnection(p, options, core)
	stopped.Store(true)
	control.Close()
	effects := map[string]int{"unload_attempts": int(unloads.Load())}
	if provider, ok := p.(interface{ Effects() map[string]int }); ok {
		effects = provider.Effects()
	}
	var transport any
	if err != nil {
		transport = fmt.Sprintf("%T", err)
	}
	event(map[string]any{"kind": "finished", "effects": childSnapshot(p, effects), "transport_error": transport})
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type childBase struct {
	transcriptBase
	count *atomic.Int32
}

func (p *childBase) Unload(context.Context) error { p.count.Add(1); return nil }

type childFull struct {
	transcriptFull
	count *atomic.Int32
	event func(any)
}

func (p *childFull) Unload(context.Context) error { p.count.Add(1); return nil }

type childLifecycleError struct {
	transcriptLifecycleError
	count *atomic.Int32
}

func (p *childLifecycleError) Unload(context.Context) error { p.count.Add(1); return nil }

type childHealthError struct {
	transcriptHealthError
	count *atomic.Int32
}

func (p *childHealthError) Unload(context.Context) error { p.count.Add(1); return nil }

type childFrameOutput struct {
	transcriptFrameOutput
	count *atomic.Int32
}

func (p *childFrameOutput) Unload(context.Context) error { p.count.Add(1); return nil }

type childShutdown struct {
	transcriptShutdown
	count *atomic.Int32
}

func (p *childShutdown) Unload(ctx context.Context) error {
	p.count.Add(1)
	return p.transcriptShutdown.Unload(ctx)
}

type childDuplex struct {
	transcriptBase
	core        *correlation
	count       *atomic.Int32
	healths     atomic.Int32
	initialized atomic.Bool
}

func (p *childDuplex) log(ctx context.Context) error {
	scope := scopeFromContext(ctx)
	id, _ := scope.id.Integer()
	raw := json.RawMessage(fmt.Sprintf(`{"grant_id":"g","context":{"binding_id":"b","timeout_ms":10000,"parent_call":{"request_owner":"host","id":%d}},"level":"info","message":"ready"}`, id))
	result, err := p.core.callContext(ctx, "host/log", raw)
	if err != nil {
		return err
	}
	return (<-result).err
}
func (p *childDuplex) Init(ctx context.Context, v InitParams) (InitResult, error) {
	if err := p.log(ctx); err != nil {
		return InitResult{}, err
	}
	p.initialized.Store(true)
	return p.transcriptBase.Init(ctx, v)
}
func (p *childDuplex) Unload(ctx context.Context) error { p.count.Add(1); return p.log(ctx) }
func (p *childDuplex) Health(context.Context) (HealthStatus, error) {
	p.healths.Add(1)
	return HealthStatus{OK: true}, nil
}
func (p *childDuplex) Effects() map[string]int {
	return map[string]int{"unload_attempts": int(p.count.Load()), "health_calls": int(p.healths.Load())}
}

func childSnapshot(p Plugin, fallback map[string]int) any {
	if value, ok := p.(interface{ Snapshot() map[string]any }); ok {
		return value.Snapshot()
	}
	return fallback
}
func (p *childDuplex) Snapshot() map[string]any {
	return map[string]any{"initialized": p.initialized.Load(), "unload_attempts": int(p.count.Load()), "health_calls": int(p.healths.Load())}
}

func (p *childFull) Command(ctx context.Context, v CommandRequest) (CommandResult, error) {
	if v.Name != "wait-for-abort" {
		return p.transcriptFull.Command(ctx, v)
	}
	p.event(map[string]any{"kind": "handler_waiting"})
	<-ctx.Done()
	return CommandResult{Action: "noop"}, nil
}
