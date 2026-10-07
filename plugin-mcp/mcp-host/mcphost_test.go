package mcphost

import (
	"context"
	"errors"
	"github.com/hollis-labs/libs/plugin-mcp/mcp-host/config"
	"testing"
)

func TestRunThreadsTrustedInitializationFactory(t *testing.T) {
	failure := errors.New("host policy refused")
	cfg := &config.Config{LogicalServers: []config.LogicalServer{{ID: "plugin", Name: "Plugin", Transport: config.TransportInprocess, Inprocess: &config.InprocessConfig{Command: "/nonexistent/plugin", Tools: []config.ToolManifest{{Name: "tool"}}}}}}
	called := false
	opts := Options{InprocessInitFactories: map[string]config.InprocessInitFactory{"plugin": func(context.Context) (config.InprocessInitialization, error) {
		called = true
		return config.InprocessInitialization{}, failure
	}}}
	if err := Run(context.Background(), cfg, opts); !errors.Is(err, failure) || !called {
		t.Fatalf("owner callback/error lost: called=%v err=%v", called, err)
	}
}
