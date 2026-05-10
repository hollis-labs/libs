package main

import (
	"context"
	"testing"

	"github.com/hollis-labs/plugin-sdk/subprocess"
	"github.com/hollis-labs/plugin-sdk/subprocess/subprocesstest"
)

// TestHelloLifecycle drives the hello plugin through init/load/unload
// using the in-process harness. Demonstrates that a plugin built
// against only plugin-sdk imports completes a full lifecycle and
// services a command.
func TestHelloLifecycle(t *testing.T) {
	h := subprocesstest.New(t, hello{})
	defer h.Close()

	ctx := context.Background()
	init, err := h.Init(ctx)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if init.ID != "hello" || init.Protocol != subprocess.ProtocolVersion {
		t.Errorf("init result = %+v", init)
	}

	if _, err := h.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	res, err := h.Command(ctx, "greet", "sess-1", "world")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Content != "hello, world" {
		t.Errorf("command content = %q, want %q", res.Content, "hello, world")
	}

	if err := h.Unload(ctx); err != nil {
		t.Fatalf("Unload: %v", err)
	}
}
