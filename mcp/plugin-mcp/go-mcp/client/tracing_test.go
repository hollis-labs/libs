package client

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/tracing"
)

// The subprocess fixture uses the SDK's stdio framing, independently of the
// HTTP roundtrip test. It echoes raw request metadata, not tool arguments.
func TestTraceContextOverStdio(t *testing.T) {
	pool := NewPool(WithTraceContext())
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.Register("fixture", extraToolsConfig(t, nil)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const parent = "00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-00"
	ctx = tracing.Extract(ctx, map[string]any{"traceparent": parent})
	res, _, err := pool.CallTool(ctx, "fixture", "meta", nil, WithCallMeta(map[string]any{"app/key": "kept"}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(resultText(t, res)), &got); err != nil {
		t.Fatal(err)
	}
	if got["traceparent"] != parent || got["app/key"] != "kept" {
		t.Fatalf("meta=%v", got)
	}
	if _, exists := got["_traceparent"]; exists {
		t.Fatal("legacy trace emitted")
	}
}
