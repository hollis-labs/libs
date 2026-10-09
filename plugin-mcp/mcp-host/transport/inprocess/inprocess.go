// Package inprocess adapts plugin-host's supervised driver to logical MCP servers.
// Tool catalogs remain manifest-driven; the plugin never implements MCP itself.
package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/libs/plugin-mcp/mcp-host/config"
	"github.com/hollis-labs/libs/plugin-mcp/mcp-host/registry"
	pluginhost "github.com/hollis-labs/libs/plugin-mcp/plugin-host"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	sdksub "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

const connectTimeout = 30 * time.Second

var (
	healthCheckInterval = 30 * time.Second
	healthCheckTimeout  = 5 * time.Second
)

// Transport owns MCP adaptation and explicit owner policy only. Process,
// wire, cancellation, health and restart ownership belong to plugin-host.
type Transport struct {
	name            string
	tools           []registry.Tool
	supervisor      *pluginhost.Supervisor
	initFactory     config.InprocessInitFactory
	initMu          sync.Mutex
	lastIncarnation capability.RuntimeIdentity
	doneCh          chan struct{}
	doneOnce        sync.Once
	closeOnce       sync.Once
	closeErr        error
}

// New starts the driver's handshake. ctx bounds startup only.
func New(ctx context.Context, name string, cfg *config.InprocessConfig, logger *slog.Logger) (*Transport, error) {
	if cfg == nil {
		return nil, fmt.Errorf("inprocess transport %q: nil config", name)
	}
	if cfg.InitFactory == nil {
		return nil, fmt.Errorf("inprocess transport %q: required owner InitFactory is missing", name)
	}
	if logger == nil {
		logger = slog.Default()
	}
	t := &Transport{name: name, tools: manifestTools(cfg.Tools), initFactory: cfg.InitFactory, doneCh: make(chan struct{})}
	startup, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	input, err := t.prepareInitialization(startup)
	if err != nil {
		return nil, fmt.Errorf("inprocess transport %q: initialization: %w", name, err)
	}
	spec := pluginhost.Spec{ID: name, ExpectedID: input.ExpectedID, ExpectedVersion: input.ExpectedVersion,
		Command: cfg.Command, Args: append([]string(nil), cfg.Args...), Env: buildEnv(cfg.Env), Init: input.Params, HandshakeTimeout: connectTimeout}
	policy := pluginhost.RestartPolicy{MaxRestarts: 5, Initial: time.Second, Max: 16 * time.Second, StableFor: time.Minute}
	if !cfg.SuperviseEnabled() {
		policy.MaxRestarts = -1
	}
	t.supervisor = pluginhost.Supervise(spec, pluginhost.SuperviseOptions{
		Policy: policy,
		InitFactory: func(ctx context.Context, attempt uint64) (sdksub.InitParams, error) {
			if attempt == 1 {
				return input.Params, nil
			}
			next, err := t.prepareInitialization(ctx)
			if err != nil {
				return sdksub.InitParams{}, err
			}
			if next.ExpectedID != input.ExpectedID || next.ExpectedVersion != input.ExpectedVersion {
				return sdksub.InitParams{}, errors.New("owner initialization changed reviewed plugin identity/version")
			}
			return next.Params, nil
		},
		// Adapter policy explicitly opts unexpected exits into bounded recovery;
		// protocol, identity and initialization refusals remain driver-terminal.
		ClassifyExit:   func(pluginhost.ExitInfo) error { return &pluginhost.TransientError{Code: "mcp_plugin_exit"} },
		HealthInterval: healthCheckInterval, HealthTimeout: healthCheckTimeout, KillAfterUnhealthy: 1,
		OnStart: func(p *pluginhost.Process) {
			logger.Info("station: plugin subprocess spawned", "server", name, "pid", p.Pid())
			for _, s := range p.LoadInfo().SkippedRegistrations {
				logger.Info("station: plugin skipped registration", "server", name, "kind", s.Kind, "id", s.ID, "reason", s.Reason)
			}
		},
		OnGiveUp: func(err error) {
			logger.Error("station: plugin supervision ended", "server", name, "err", err)
			t.doneOnce.Do(func() { close(t.doneCh) })
		},
	})
	if err := t.supervisor.Start(startup); err != nil {
		return nil, fmt.Errorf("inprocess transport %q: initial spawn: %w", name, err)
	}
	return t, nil
}
func (t *Transport) ListTools(context.Context) ([]registry.Tool, error) {
	return append([]registry.Tool(nil), t.tools...), nil
}
func manifestTools(ms []config.ToolManifest) []registry.Tool {
	out := make([]registry.Tool, len(ms))
	for i, m := range ms {
		out[i] = registry.Tool{Name: m.Name, Description: m.Description, InputSchema: m.InputSchema, Annotations: m.Annotations}
	}
	return out
}
func (t *Transport) CallTool(ctx context.Context, name string, args map[string]any) (*registry.ToolResult, error) {
	p := t.supervisor.Current()
	if p == nil {
		return nil, fmt.Errorf("inprocess transport %q: not connected", t.name)
	}
	res, err := p.Client().MCPCallTool(ctx, sdksub.MCPCallRequest{ToolName: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("inprocess transport %q: mcp/call_tool %s: %w", t.name, name, err)
	}
	out := &registry.ToolResult{IsError: res.IsError}
	if len(res.Content) > 0 {
		out.Content = []registry.ToolContent{{Type: "text", Text: string(res.Content)}}
	}
	return out, nil
}
func (t *Transport) RestartCount() int64 { return int64(t.supervisor.Restarts()) }
func (t *Transport) Pid() int {
	if p := t.supervisor.Current(); p != nil {
		return p.Pid()
	}
	return 0
}

// Status exposes the driver's typed failure, exhaustion and exit status.
func (t *Transport) Status() pluginhost.SupervisorStatus { return t.supervisor.Status() }

// Close ends supervision and delegates unload/kill/reap to the sole driver.
func (t *Transport) Close() error {
	t.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		t.closeErr = t.supervisor.Stop(ctx)
		t.doneOnce.Do(func() { close(t.doneCh) })
	})
	return t.closeErr
}

type initializationError struct{ cause error }

func (e *initializationError) Error() string { return "owner initialization: " + e.cause.Error() }
func (e *initializationError) Unwrap() error { return e.cause }

func (t *Transport) prepareInitialization(ctx context.Context) (config.InprocessInitialization, error) {
	fail := func(err error) (config.InprocessInitialization, error) {
		return config.InprocessInitialization{}, &initializationError{err}
	}
	if t.initFactory == nil {
		return fail(errors.New("required InitFactory is missing"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	input, err := t.initFactory(ctx)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if !utf8.ValidString(input.ExpectedID) || strings.TrimSpace(input.ExpectedID) == "" ||
		!utf8.ValidString(input.ExpectedVersion) || strings.TrimSpace(input.ExpectedVersion) == "" {
		return fail(errors.New("expected plugin ID and version are required"))
	}
	if _, err := pluginhost.CompareVersions(input.ExpectedVersion, input.ExpectedVersion); err != nil {
		return fail(fmt.Errorf("expected plugin version: %w", err))
	}
	if input.Params.Grants == nil {
		return fail(errors.New("explicit grant array is required (empty is allowed)"))
	}
	if input.Params.HostServices != nil || input.Params.HooksProfile != nil {
		return fail(errors.New("inprocess is forward-only; reverse and hooks offers are unsupported"))
	}
	// Canonical marshaling validates the complete SDK contract, then decoding
	// takes ownership of all maps, slices and raw values before any process exists.
	raw, err := json.Marshal(input.Params)
	if err != nil {
		return fail(err)
	}
	var snapshot sdksub.InitParams
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	t.initMu.Lock()
	defer t.initMu.Unlock()
	previous, next := t.lastIncarnation, snapshot.Incarnation
	if previous != (capability.RuntimeIdentity{}) &&
		(next.HostInstance != previous.HostInstance || next.OwnerID != previous.OwnerID || next.OwnerGeneration <= previous.OwnerGeneration) {
		return fail(errors.New("fresh same-owner incarnation generation is required"))
	}
	t.lastIncarnation = next
	input.Params = snapshot
	return input, nil
}

// Inheritance remains an explicit adapter choice; the driver never implicitly inherits.
func buildEnv(env map[string]string) []string {
	base := os.Environ()
	extra := make([]string, 0, len(env))
	for k, v := range env {
		extra = append(extra, k+"="+v)
	}
	sort.Strings(extra)
	return append(base, extra...)
}
