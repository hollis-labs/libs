package subprocesstest_test

import (
	"context"
	"os"
	"testing"

	plugin "github.com/hollis-labs/plugin-sdk"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"github.com/hollis-labs/plugin-sdk/subprocess/subprocesstest"
)

type echoPlugin struct{}

func (echoPlugin) Init(ctx context.Context, p subprocess.InitParams) (subprocess.InitResult, error) {
	return subprocess.InitResult{
		ID: "echo", Name: "Echo", Version: "0.0.1",
		Description: "test", Protocol: subprocess.ProtocolVersion,
	}, nil
}

func (echoPlugin) Load(ctx context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}

func (echoPlugin) Unload(ctx context.Context) error { return nil }

func (echoPlugin) Command(ctx context.Context, req subprocess.CommandRequest) (subprocess.CommandResult, error) {
	return subprocess.CommandResult{Action: "message", Content: "echo: " + req.Args}, nil
}

func TestHarnessBasicLifecycle(t *testing.T) {
	h := subprocesstest.New(t, echoPlugin{})
	defer h.Close()

	ctx := context.Background()
	init, err := h.Init(ctx)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if init.ID != "echo" {
		t.Errorf("init id = %q, want echo", init.ID)
	}
	if _, err := h.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	res, err := h.Command(ctx, "say", "session-1", "hello")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Action != "message" || res.Content != "echo: hello" {
		t.Errorf("command result = %+v", res)
	}

	if err := h.Unload(ctx); err != nil {
		t.Fatalf("Unload: %v", err)
	}
}

func TestHarnessWithJSONRoundtrip(t *testing.T) {
	h := subprocesstest.New(t, echoPlugin{}, subprocesstest.WithJSONRoundtrip(true))
	defer h.Close()

	if !h.RoundtripEnabled() {
		t.Fatal("roundtrip not enabled")
	}

	ctx := context.Background()
	if _, err := h.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	res, err := h.Command(ctx, "x", "s", "y")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Content != "echo: y" {
		t.Errorf("roundtrip broke payload: %+v", res)
	}
}

// brokenPlugin returns a CommandResult whose Envelopes contain a
// channel in the Data map — channels cannot be marshaled to JSON, so
// roundtrip mode must surface this as an error rather than silently
// succeeding (which a normal in-process test would).
type brokenPlugin struct{}

func (brokenPlugin) Init(ctx context.Context, p subprocess.InitParams) (subprocess.InitResult, error) {
	return subprocess.InitResult{ID: "broken", Protocol: subprocess.ProtocolVersion}, nil
}
func (brokenPlugin) Load(ctx context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (brokenPlugin) Unload(ctx context.Context) error { return nil }
func (brokenPlugin) Command(ctx context.Context, req subprocess.CommandRequest) (subprocess.CommandResult, error) {
	return subprocess.CommandResult{
		Action:  "message",
		Content: "ok",
		Envelopes: []plugin.EnvelopeOut{
			{Type: "broken", Data: map[string]interface{}{"ch": make(chan int)}},
		},
	}, nil
}

func TestRoundtripCatchesUnserializableResult(t *testing.T) {
	// Roundtrip OFF: unserializable payload slides through the harness
	// and the test would pass silently — exactly the bug class we want
	// to catch. Explicitly disable because CI may set the env var.
	h := subprocesstest.New(t, brokenPlugin{}, subprocesstest.WithJSONRoundtrip(false))
	defer h.Close()
	if _, err := h.Command(context.Background(), "x", "", ""); err != nil {
		t.Fatalf("expected success without roundtrip, got %v", err)
	}

	// Roundtrip ON: marshal fails on the unserializable channel and
	// the harness surfaces the error.
	h2 := subprocesstest.New(t, brokenPlugin{}, subprocesstest.WithJSONRoundtrip(true))
	defer h2.Close()
	if _, err := h2.Command(context.Background(), "x", "", ""); err == nil {
		t.Errorf("expected roundtrip to catch unserializable payload, got no error")
	}
}

func TestEnvTruthyDefault(t *testing.T) {
	_ = os.Unsetenv("NANITE_PLUGIN_SDK_JSON_ROUNDTRIP")
	h := subprocesstest.New(t, echoPlugin{})
	if h.RoundtripEnabled() {
		t.Errorf("roundtrip should default off when env is unset")
	}
}

func TestEnvTruthyEnabled(t *testing.T) {
	t.Setenv("NANITE_PLUGIN_SDK_JSON_ROUNDTRIP", "1")
	h := subprocesstest.New(t, echoPlugin{})
	if !h.RoundtripEnabled() {
		t.Errorf("roundtrip should be enabled via env var")
	}
}

func TestHarnessCommandOnNonHandlerPlugin(t *testing.T) {
	h := subprocesstest.New(t, &minimalPlugin{})
	defer h.Close()
	if _, err := h.Command(context.Background(), "x", "", ""); err == nil {
		t.Fatal("expected error when plugin lacks CommandHandler")
	}
}

type minimalPlugin struct{}

func (*minimalPlugin) Init(ctx context.Context, p subprocess.InitParams) (subprocess.InitResult, error) {
	return subprocess.InitResult{ID: "min", Protocol: subprocess.ProtocolVersion}, nil
}
func (*minimalPlugin) Load(ctx context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (*minimalPlugin) Unload(ctx context.Context) error { return nil }
