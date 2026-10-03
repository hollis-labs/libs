package hookstest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
	"github.com/hollis-labs/plugin-hooks/hookstest"
)

// No waivers: the reference dispatcher must meet every published requirement.
func TestConformance(t *testing.T) { hookstest.Run(t, hookstest.NewEngineAdapter) }

func TestPluginHarness(t *testing.T) {
	remote := false
	definition := hooks.Definition{Name: "example.ready", Kind: hooks.Action, Mode: hooks.Sequential, Since: "1.0.0", SchemaDigest: "example-v1", RemoteOK: &remote, InputSchema: json.RawMessage(`{}`), ValidateInput: func(json.RawMessage) error { return nil }, Budget: time.Second, HandlerTimeout: time.Second, OnErrorDefault: hooks.Closed, AllowedOnError: []hooks.ErrorPolicy{hooks.Closed}, MaxPayloadBytes: 1024, MaxHandlers: 8, MaxParallelism: 1}
	h, err := hookstest.NewHarness(hookstest.NewEngineAdapter, hooks.Catalog{Version: "1", Definitions: []hooks.Definition{definition}}, hooks.ExecutionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if closeErr := h.Close(cleanup); closeErr != nil {
			t.Error(closeErr)
		}
	})
	calls := 0
	scope, err := h.Load(ctx, hooks.ScopeConfig{Owner: "example", Generation: "one", Hooks: []string{definition.Name}}, func(s hookstest.Scope) error {
		_, addErr := s.AddAction(definition.Name, "observe", hooks.Options{}, func(context.Context, hooks.Invocation) error { calls++; return nil })
		return addErr
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.EmitAction(ctx, definition.Name, json.RawMessage(`{}`), nil)
	if err != nil || result.Status != hooks.Success || calls != 1 {
		t.Fatal(result, err, calls)
	}
	if disposeErr := scope.Dispose(ctx); disposeErr != nil {
		t.Fatal(disposeErr)
	}
	_, err = h.EmitAction(ctx, definition.Name, json.RawMessage(`{}`), nil)
	if err != nil || calls != 1 {
		t.Fatal("unloaded plugin called", err)
	}
	failure := errors.New("registration failed")
	_, err = h.Load(ctx, hooks.ScopeConfig{Owner: "example", Generation: "two", Hooks: []string{definition.Name}}, func(s hookstest.Scope) error {
		_, addErr := s.AddAction(definition.Name, "partial", hooks.Options{}, func(context.Context, hooks.Invocation) error { calls++; return nil })
		if addErr != nil {
			return addErr
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	_, err = h.EmitAction(ctx, definition.Name, json.RawMessage(`{}`), nil)
	if err != nil || calls != 1 {
		t.Fatal("failed load left a registration", err)
	}
	if closeErr := h.Close(ctx); closeErr != nil {
		t.Fatal(closeErr)
	}
	_, err = h.Load(ctx, hooks.ScopeConfig{Owner: "example", Generation: "three"}, func(hookstest.Scope) error { return nil })
	if !errors.Is(err, hooks.ErrDisposed) {
		t.Fatal("load after close", err)
	}
}
