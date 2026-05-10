// Package subprocesstest provides an in-process test harness for
// subprocess plugins. Harness wraps a plugin and calls its methods
// directly (no subprocess, no JSON-RPC) so plugin authors can write
// fast, deterministic unit tests against the SDK contract.
//
// For wire-format fidelity, enable JSON roundtrip mode — either
// explicitly with WithJSONRoundtrip(true) or via the environment
// variable PLUGIN_SDK_JSON_ROUNDTRIP=1. In roundtrip mode every
// request and response is run through json.Marshal + json.Unmarshal
// before and after the plugin sees it, catching missed json tags,
// unserializable types, and other wire-only bugs that would otherwise
// only surface in production.
//
// The legacy NANITE_PLUGIN_SDK_JSON_ROUNDTRIP env var name is still
// honored for backward compatibility but is deprecated; prefer the
// host-neutral PLUGIN_SDK_JSON_ROUNDTRIP.
//
// Example:
//
//	h := subprocesstest.New(t, &myPlugin{},
//	    subprocesstest.WithConfig(map[string]string{"api_key": "test"}),
//	)
//	defer h.Close()
//
//	if _, err := h.Init(ctx); err != nil { t.Fatal(err) }
//	if _, err := h.Load(ctx); err != nil { t.Fatal(err) }
//
//	res, err := h.Command(ctx, "greet", "session-1", "world")
//	require.NoError(t, err)
//	require.Equal(t, "message", res.Action)
package subprocesstest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// envJSONRoundtrip is the canonical env var that forces JSON roundtrip
// on every request/response when set to a truthy value (any non-empty
// string other than "0" or "false").
const envJSONRoundtrip = "PLUGIN_SDK_JSON_ROUNDTRIP"

// envJSONRoundtripLegacy is the original (host-coupled) env var name.
// Honored for backward compatibility; prefer envJSONRoundtrip in new
// configurations. Will be removed in a future release.
const envJSONRoundtripLegacy = "NANITE_PLUGIN_SDK_JSON_ROUNDTRIP"

// Harness drives a subprocess plugin in-process with mocked init
// parameters. Call Init, Load, Unload, and capability-specific helpers
// (Command, Event, CRUD*) to exercise the plugin under test.
type Harness struct {
	t         testing.TB
	plugin    subprocess.Plugin
	pluginDir string
	tempDir   string
	config    map[string]string
	hostInfo  subprocess.HostInfo
	roundtrip bool
}

// Option configures a Harness during New.
type Option func(*Harness)

// WithConfig seeds the resolved config map passed to the plugin's Init
// via InitParams.Config.
func WithConfig(cfg map[string]string) Option {
	return func(h *Harness) {
		h.config = cfg
	}
}

// WithPluginDir overrides the harness's default temp plugin directory.
// Use this when the plugin loads fixtures from specific on-disk paths.
func WithPluginDir(dir string) Option {
	return func(h *Harness) {
		h.pluginDir = dir
	}
}

// WithHostInfo overrides the default HostInfo passed in InitParams.
// Defaults are version="test" and protocol=ProtocolVersion.
func WithHostInfo(info subprocess.HostInfo) Option {
	return func(h *Harness) {
		h.hostInfo = info
	}
}

// WithJSONRoundtrip forces every request/response to be marshaled and
// re-unmarshaled through JSON before the plugin sees it (and again on
// the way out). Catches wire-format bugs — missing json tags,
// unserializable fields — that would otherwise only surface when the
// plugin runs as a real subprocess.
//
// If not set, the environment variable PLUGIN_SDK_JSON_ROUNDTRIP is
// consulted (with NANITE_PLUGIN_SDK_JSON_ROUNDTRIP honored as a
// deprecated legacy alias); any truthy value enables roundtripping.
func WithJSONRoundtrip(enabled bool) Option {
	return func(h *Harness) {
		h.roundtrip = enabled
	}
}

// New constructs a Harness around the given plugin. If t is non-nil,
// temp directories are cleaned up on test completion via t.Cleanup.
// Otherwise, call Close explicitly.
func New(t testing.TB, p subprocess.Plugin, opts ...Option) *Harness {
	h := &Harness{
		t:         t,
		plugin:    p,
		config:    map[string]string{},
		hostInfo:  subprocess.HostInfo{Version: "test", Protocol: subprocess.ProtocolVersion},
		roundtrip: envTruthy(os.Getenv(envJSONRoundtrip)) || envTruthy(os.Getenv(envJSONRoundtripLegacy)),
	}
	for _, opt := range opts {
		opt(h)
	}
	if h.pluginDir == "" {
		dir, err := os.MkdirTemp("", "plugin-sdk-harness-")
		if err != nil {
			if t != nil {
				t.Fatalf("mkdir temp: %v", err)
			}
			panic(fmt.Errorf("harness temp dir: %w", err))
		}
		h.pluginDir = dir
		h.tempDir = dir
		if t != nil {
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
		}
	}
	return h
}

// Close removes any temp directories the harness created. Safe to call
// multiple times; a no-op if WithPluginDir was used.
func (h *Harness) Close() error {
	if h.tempDir != "" {
		err := os.RemoveAll(h.tempDir)
		h.tempDir = ""
		return err
	}
	return nil
}

// PluginDir returns the directory being used as the plugin's home.
// Plugins can write fixture files under this path in tests.
func (h *Harness) PluginDir() string { return h.pluginDir }

// DataDir returns a conventional data subdirectory under PluginDir.
func (h *Harness) DataDir() string { return filepath.Join(h.pluginDir, "data") }

// Init invokes the plugin's Init method with the harness's mocked
// InitParams (PluginDir + resolved config + default HostInfo).
func (h *Harness) Init(ctx context.Context) (subprocess.InitResult, error) {
	params := subprocess.InitParams{
		PluginDir: h.pluginDir,
		Config:    h.config,
		HostInfo:  h.hostInfo,
	}
	if err := h.maybeRoundtrip(&params); err != nil {
		return subprocess.InitResult{}, err
	}
	res, err := h.plugin.Init(ctx, params)
	if err != nil {
		return res, err
	}
	if err := h.maybeRoundtrip(&res); err != nil {
		return res, err
	}
	return res, nil
}

// Load invokes the plugin's Load method.
func (h *Harness) Load(ctx context.Context) (subprocess.LoadResult, error) {
	res, err := h.plugin.Load(ctx)
	if err != nil {
		return res, err
	}
	if err := h.maybeRoundtrip(&res); err != nil {
		return res, err
	}
	return res, nil
}

// Unload invokes the plugin's Unload method.
func (h *Harness) Unload(ctx context.Context) error {
	return h.plugin.Unload(ctx)
}

// Command invokes the plugin's Command capability if implemented.
// Returns an error containing "not implemented" if the plugin does not
// satisfy the CommandHandler interface.
func (h *Harness) Command(ctx context.Context, name, sessionID, args string) (subprocess.CommandResult, error) {
	ch, ok := h.plugin.(subprocess.CommandHandler)
	if !ok {
		return subprocess.CommandResult{}, fmt.Errorf("plugin does not implement CommandHandler")
	}
	req := subprocess.CommandRequest{Name: name, SessionID: sessionID, Args: args}
	if err := h.maybeRoundtrip(&req); err != nil {
		return subprocess.CommandResult{}, err
	}
	res, err := ch.Command(ctx, req)
	if err != nil {
		return res, err
	}
	if err := h.maybeRoundtrip(&res); err != nil {
		return res, err
	}
	return res, nil
}

// Event invokes the plugin's EventHandle capability if implemented.
func (h *Harness) Event(ctx context.Context, req subprocess.EventRequest) (subprocess.EventResult, error) {
	eh, ok := h.plugin.(subprocess.EventHandler)
	if !ok {
		return subprocess.EventResult{}, fmt.Errorf("plugin does not implement EventHandler")
	}
	if err := h.maybeRoundtrip(&req); err != nil {
		return subprocess.EventResult{}, err
	}
	res, err := eh.EventHandle(ctx, req)
	if err != nil {
		return res, err
	}
	if err := h.maybeRoundtrip(&res); err != nil {
		return res, err
	}
	return res, nil
}

// Health invokes the plugin's Health capability if implemented.
// Plugins that do not implement HealthChecker are reported as OK.
func (h *Harness) Health(ctx context.Context) (subprocess.HealthStatus, error) {
	hc, ok := h.plugin.(subprocess.HealthChecker)
	if !ok {
		return subprocess.HealthStatus{OK: true}, nil
	}
	return hc.Health(ctx)
}

// RoundtripEnabled reports whether JSON roundtripping is active for
// this harness instance. Useful in tests that want to assert the
// harness was configured the way the caller expected.
func (h *Harness) RoundtripEnabled() bool { return h.roundtrip }

// maybeRoundtrip runs v through json.Marshal + json.Unmarshal when
// roundtrip mode is enabled. v must be a non-nil pointer; the result
// is written back through the pointer. No-op when roundtrip is off.
func (h *Harness) maybeRoundtrip(v any) error {
	if !h.roundtrip {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("roundtrip marshal: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("roundtrip unmarshal: %w", err)
	}
	return nil
}

// envTruthy reports whether a raw env-var string should be interpreted
// as true. "", "0", and case-insensitive "false" are false; everything
// else is true.
func envTruthy(s string) bool {
	switch s {
	case "", "0", "false", "False", "FALSE":
		return false
	}
	return true
}
