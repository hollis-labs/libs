package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
	"github.com/hollis-labs/plugin-hooks/hookstest"
	"github.com/hollis-labs/plugin-sdk/capability"
	sdk "github.com/hollis-labs/plugin-sdk/subprocess"
)

type childReply struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *sdk.RPCError   `json:"error"`
}
type child struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan childReply
	done    chan struct{}
	writeMu sync.Mutex
	queue   time.Duration
}

func startChild(runtime string, identity capability.RuntimeIdentity) (*child, error) {
	source := os.Getenv("SDK_TEST_SOURCE")
	if source == "" {
		return nil, errors.New("run scripts/prepare.sh before tests")
	}
	var cmd *exec.Cmd
	if runtime == "Go" {
		cmd = exec.Command(os.Getenv("SDK_TEST_GO_CHILD"), "-test.run=^TestHookChild$")
		cmd.Env = append(os.Environ(), "HOOK_TEST_CHILD=hooks-fixture")
	} else {
		cmd = exec.Command("node", filepath.Join(source, "ts/packages/plugin-sdk/test/hooks-child.js"), "hooks-fixture")
	}
	cmd.Dir = source
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	c := &child{cmd: cmd, in: in, pending: map[uint64]chan childReply{}, done: make(chan struct{})}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer close(c.done)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			var r childReply
			if json.Unmarshal(scanner.Bytes(), &r) != nil {
				return
			}
			c.mu.Lock()
			ch := c.pending[r.ID]
			delete(c.pending, r.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- r
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	init := map[string]any{"plugin_dir": "fixture", "data_dir": "fixture", "cache_dir": "fixture", "config": map[string]any{}, "log_level": "info", "host_info": map[string]any{"version": "fixture", "protocol": 2}, "capability_contract": 1, "incarnation": identity, "grants": []any{}}
	raw, _ := json.Marshal(init)
	if _, err = c.call(ctx, "plugin/init", raw, false); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}
func (c *child) call(ctx context.Context, method string, params []byte, notification bool) (json.RawMessage, error) {
	queued := time.Duration(0)
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan childReply, 1)
	if !notification {
		c.pending[id] = ch
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.queue += queued
		c.mu.Unlock()
	}()
	// Splice SDK-encoded DTO instead of normalizing opaque payload literals.
	frame := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":`, id, method)
	if notification {
		frame = fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":`, method)
	}
	// Normalize nonliteral framing whitespace without parsing/re-encoding payloads.
	// json.Compact retains numeric, string and escaped-key tokens verbatim.
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(frame+string(params)+"}")); err != nil {
		return nil, err
	}
	if compact.Len() >= 8<<20 {
		return nil, errors.New("test frame exceeds SDK input bound")
	}
	writeStart := time.Now()
	c.writeMu.Lock()
	queued = time.Since(writeStart)
	_, err := io.WriteString(c.in, compact.String()+"\n")
	c.writeMu.Unlock()
	if err != nil {
		return nil, err
	}
	if notification {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, io.EOF
	case r := <-ch:
		if r.Error != nil {
			return nil, fmt.Errorf("RPC failure %d", r.Error.Code)
		}
		return r.Result, nil
	}
}
func (c *child) close() {
	_ = c.in.Close()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}

type childPool struct {
	runtime      string
	mu           sync.Mutex
	children     map[capability.RuntimeIdentity]*child
	incarnations map[scopeKey]capability.RuntimeIdentity
	generations  map[ownerKey]uint64
}

func (p *childPool) get(identity capability.RuntimeIdentity) (*child, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.children[identity]; c != nil {
		return c, nil
	}
	c, err := startChild(p.runtime, identity)
	if err != nil {
		return nil, err
	}
	p.children[identity] = c
	return c, nil
}

type scopeKey struct{ host, owner, generation string }
type ownerKey struct{ host, owner string }

// This is a host-owned ledger assigning wire incarnations to opaque generations.
// No generation string is parsed, and no plugin-controlled field grants a scope.
func (p *childPool) identity(q hooks.RemoteRequest) capability.RuntimeIdentity {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := scopeKey{q.Scope.HostInstance, q.Scope.Owner, q.Scope.Generation}
	if inc, ok := p.incarnations[key]; ok {
		return inc
	}
	if p.incarnations == nil {
		p.incarnations = map[scopeKey]capability.RuntimeIdentity{}
		p.generations = map[ownerKey]uint64{}
	}
	owner := ownerKey{q.Scope.HostInstance, q.Scope.Owner}
	p.generations[owner]++
	inc := capability.RuntimeIdentity{HostInstance: owner.host, OwnerID: owner.owner, OwnerGeneration: p.generations[owner]}
	p.incarnations[key] = inc
	return inc
}
func (p *childPool) handle(handler hooks.RemoteHandler) hooks.RemoteHandler {
	return hooks.RemoteHandlerFunc(func(ctx context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
		// Host-side callback simulation: closures retain the engine-issued binding.
		// The child sees the real forward request; it never originates reverse RPC.
		inc := p.identity(q)
		wire, err := Request(q, inc)
		if err != nil {
			return hooks.RemoteResult{}, err
		}
		c, err := p.get(inc)
		if err != nil {
			return hooks.RemoteResult{}, err
		}
		raw, err := sdk.EncodeHookHandleParams(wire)
		if err != nil {
			return hooks.RemoteResult{}, err
		}
		if _, err = c.call(ctx, sdk.MethodHookHandle, raw, false); err != nil {
			return hooks.RemoteResult{}, err
		}
		result, callErr := handler.Handle(ctx, q)
		wire.Metadata = cloneMetadata(wire.Metadata)
		if callErr != nil {
			wire.Metadata["fixture"] = "error"
		} else {
			wire.Metadata["fixture"], wire.Metadata["script"] = directive(result, wire)
		}
		raw, err = sdk.EncodeHookHandleParams(wire)
		if err != nil {
			return hooks.RemoteResult{}, err
		}
		reply, err := c.call(ctx, sdk.MethodHookHandle, raw, false)
		if err != nil {
			return hooks.RemoteResult{}, err
		}
		if callErr != nil {
			return hooks.RemoteResult{}, callErr
		}
		return Result(wire, reply)
	})
}
func directive(r hooks.RemoteResult, p sdk.HookHandleParams) (string, string) {
	if r.InvocationID != p.InvocationID {
		return "raw:wrong-id", ""
	}
	if r.Status == "success" {
		return "raw:unknown-status", ""
	}
	if r.Failure != nil && r.Failure.Code == "cancelled" {
		return "raw:unknown-code", ""
	}
	if r.Status == hooks.RemoteOK && p.Kind == "action" && r.Payload != nil {
		return "raw:action-output", ""
	}
	if r.Status != hooks.RemoteOK && r.Payload != nil {
		return "raw:non-ok-output", ""
	}
	raw, err := WireResult(p, r)
	if err != nil {
		return "invalid_output", ""
	}
	return "script", string(raw)
}
func (p *childPool) notify(notifier hooks.RemoteNotifier) hooks.RemoteNotifier {
	return hooks.RemoteNotifierFunc(func(ctx context.Context, n hooks.RemoteNotification) error {
		inc := p.identity(n.Request)
		wire, err := Notification(n, inc)
		if err != nil {
			return err
		}
		c, err := p.get(inc)
		if err != nil {
			return err
		}
		raw, err := sdk.EncodeHookHandleParams(wire)
		if err != nil {
			return err
		}
		if _, err = c.call(ctx, sdk.MethodHookHandle, raw, true); err != nil {
			return err
		}
		return notifier.Notify(ctx, n)
	})
}
func (p *childPool) batch(handler hooks.RemoteBatchHandler) hooks.RemoteBatchHandler {
	return hooks.RemoteBatchHandlerFunc(func(ctx context.Context, requests []hooks.RemoteRequest) ([]hooks.RemoteResult, error) {
		ids := make([]capability.RuntimeIdentity, len(requests))
		for i, q := range requests {
			ids[i] = p.identity(q)
		}
		wire, err := BatchRequest(requests, ids)
		if err != nil {
			return nil, err
		}
		c, err := p.get(ids[0])
		if err != nil {
			return nil, err
		}
		raw, err := sdk.EncodeHookHandleBatchParams(wire)
		if err != nil {
			return nil, err
		}
		if _, err = c.call(ctx, sdk.MethodHookHandleBatch, raw, false); err != nil {
			return nil, err
		}
		results, err := handler.HandleBatch(ctx, requests)
		if err != nil {
			return nil, err
		}
		if len(results) != len(wire.Items) {
			return nil, hooks.ErrInvalidOutput
		}
		for i, r := range results {
			wire.Items[i].Metadata = cloneMetadata(wire.Items[i].Metadata)
			wire.Items[i].Metadata["fixture"], wire.Items[i].Metadata["script"] = directive(r, wire.Items[i])
		}
		raw, err = sdk.EncodeHookHandleBatchParams(wire)
		if err != nil {
			return nil, err
		}
		reply, err := c.call(ctx, sdk.MethodHookHandleBatch, raw, false)
		if err != nil {
			return nil, err
		}
		return BatchResult(wire, reply)
	})
}

type wireDispatcher struct {
	registry *hooks.Registry
	engine   *hooks.Engine
	pool     *childPool
}

func (d wireDispatcher) NewScope(c hooks.ScopeConfig) (hookstest.Scope, error) {
	scope, err := d.registry.NewScope(c)
	if err != nil {
		return nil, err
	}
	if c.Remote {
		// Host startup/Init precedes admission. Never spend a handler lease starting
		// a fixture child; conformance's latency/deadline ceilings remain unchanged.
		inc := d.pool.identity(hooks.RemoteRequest{Scope: hooks.RemoteScope{HostInstance: d.registry.HostInstance(), Owner: c.Owner, Generation: c.Generation}})
		if _, err = d.pool.get(inc); err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = scope.Dispose(ctx)
			return nil, err
		}
	}
	return wireScope{scope: scope, pool: d.pool}, nil
}
func (d wireDispatcher) EmitAction(ctx context.Context, h string, p json.RawMessage, m map[string]string) (hooks.DispatchResult, error) {
	return d.engine.EmitAction(ctx, h, p, m)
}
func (d wireDispatcher) ApplyFilters(ctx context.Context, h string, p json.RawMessage, m map[string]string) (hooks.DispatchResult, error) {
	return d.engine.ApplyFilters(ctx, h, p, m)
}
func (d wireDispatcher) Close(ctx context.Context) error { return d.engine.Shutdown(ctx) }
func (d wireDispatcher) Breaker(owner, generation string) (hooks.BreakerSnapshot, error) {
	return d.engine.Breaker(owner, generation)
}
func (d wireDispatcher) ResetBreaker(owner, generation string) error {
	return d.engine.ResetBreaker(owner, generation)
}
func (d wireDispatcher) RemoteCallbackContext(ctx context.Context, connection, binding string) (context.Context, context.CancelFunc, error) {
	return d.engine.RemoteCallbackContext(ctx, connection, binding)
}
func nativeItems(items []hookstest.BatchItem) ([]hooks.RemoteBatchItem, error) {
	out := make([]hooks.RemoteBatchItem, len(items))
	for i, item := range items {
		h, ok := item.Handle.(hooks.Handle)
		if !ok {
			return nil, hooks.ErrInvalidOptions
		}
		out[i] = hooks.RemoteBatchItem{Handle: h, Payload: item.Payload, Metadata: item.Metadata}
	}
	return out, nil
}
func (d wireDispatcher) EmitRemoteBatch(ctx context.Context, h hooks.RemoteBatchHandler, items []hookstest.BatchItem) ([]hooks.RemoteBatchOutcome, error) {
	native, err := nativeItems(items)
	if err != nil {
		return nil, err
	}
	return d.engine.EmitRemoteBatch(ctx, d.pool.batch(h), native)
}
func (d wireDispatcher) PrepareAfterCommit(ctx context.Context, h string, p json.RawMessage, m map[string]string) (hookstest.Commit, error) {
	return d.engine.PrepareAfterCommit(ctx, h, p, m)
}
func (d wireDispatcher) PrepareRemoteBatchAfterCommit(ctx context.Context, h hooks.RemoteBatchHandler, items []hookstest.BatchItem) (hookstest.BatchCommit, error) {
	native, err := nativeItems(items)
	if err != nil {
		return nil, err
	}
	return d.engine.PrepareRemoteBatchAfterCommit(ctx, d.pool.batch(h), native)
}

type wireScope struct {
	scope *hooks.Scope
	pool  *childPool
}

func (s wireScope) AddAction(h, n string, o hooks.Options, f hooks.ActionFunc) (hookstest.Handle, error) {
	return s.scope.AddAction(h, n, o, f)
}
func (s wireScope) AddFilter(h, n string, o hooks.Options, f hooks.FilterFunc) (hookstest.Handle, error) {
	return s.scope.AddFilter(h, n, o, f)
}
func (s wireScope) Remove(token hookstest.Handle) {
	if h, ok := token.(hooks.Handle); ok {
		s.scope.Remove(h)
	}
}
func (s wireScope) Dispose(ctx context.Context) error { return s.scope.Dispose(ctx) }
func (s wireScope) registration(r hooks.RemoteRegistration) hooks.RemoteRegistration {
	if r.Handler != nil {
		r.Handler = s.pool.handle(r.Handler)
	}
	if r.Notifier != nil {
		r.Notifier = s.pool.notify(r.Notifier)
	}
	return r
}
func (s wireScope) AddRemoteAction(h, n string, o hooks.Options, r hooks.RemoteRegistration) (hookstest.Handle, error) {
	return s.scope.AddRemoteAction(h, n, o, s.registration(r))
}
func (s wireScope) AddRemoteFilter(h, n string, o hooks.Options, r hooks.RemoteRegistration) (hookstest.Handle, error) {
	return s.scope.AddRemoteFilter(h, n, o, s.registration(r))
}

func TestRealChildren(t *testing.T) {
	for _, runtime := range []string{"Go", "Node"} {
		t.Run(runtime+"/hooks_profile_version=1 WITHOUT plugin-originated callbacks (reverse lane not exercised or claimed)", func(t *testing.T) {
			pool := &childPool{runtime: runtime, children: map[capability.RuntimeIdentity]*child{}}
			t.Cleanup(func() {
				pool.mu.Lock()
				defer pool.mu.Unlock()
				for _, c := range pool.children {
					c.close()
				}
			})
			factory := func(c hooks.Catalog, config hooks.ExecutionConfig) (hookstest.Dispatcher, error) {
				registry, err := hooks.NewRegistry(c)
				if err != nil {
					return nil, err
				}
				engine, err := hooks.NewEngine(registry, config)
				if err != nil {
					return nil, err
				}
				return wireDispatcher{registry: registry, engine: engine, pool: pool}, nil
			}
			hookstest.Run(t, factory)
		})
	}
}
