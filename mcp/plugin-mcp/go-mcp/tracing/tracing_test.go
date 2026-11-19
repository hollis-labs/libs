package tracing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/tracing"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const parent = "00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01"

func TestCarrier(t *testing.T) {
	type key struct{}
	base, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "kept"), time.Second)
	defer cancel()
	ctx := tracing.Extract(base, map[string]any{"traceparent": parent, "tracestate": "vendor=value", "baggage": "secret=value"})
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() || !sc.IsRemote() || !sc.IsSampled() || sc.TraceState().String() != "vendor=value" {
		t.Fatalf("context = %v", sc)
	}
	if ctx.Value(key{}) != "kept" || ctx.Done() != base.Done() {
		t.Fatal("lost request context")
	}
	if baggage.FromContext(ctx).Len() != 0 {
		t.Fatal("extracted baggage")
	}
	original := map[string]any{"app/key": "keep", "traceparent": "stale", "_traceparent": "old", "baggage": "untouched"}
	got := tracing.Inject(ctx, original)
	if got["traceparent"] != parent || got["tracestate"] != "vendor=value" || got["app/key"] != "keep" {
		t.Fatalf("meta = %v", got)
	}
	if original["traceparent"] != "stale" {
		t.Fatal("mutated caller map")
	}
	if _, ok := got["_traceparent"]; ok {
		t.Fatal("sent legacy parent")
	}
	noState := trace.ContextWithSpanContext(base, sc.WithTraceState(trace.TraceState{}))
	if _, ok := tracing.Inject(noState, got)["tracestate"]; ok {
		t.Fatal("kept stale tracestate")
	}
	if tracing.Inject(base, nil) != nil {
		t.Fatal("untraced call allocated meta")
	}
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("lost cancellation")
	}
}

func TestInvalidAndLegacyCarriers(t *testing.T) {
	for _, v := range []any{nil, 42, "", "bad", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", "00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01"} {
		ctx := tracing.Extract(context.Background(), map[string]any{"traceparent": v, "_traceparent": parent})
		if trace.SpanContextFromContext(ctx).IsValid() {
			t.Errorf("accepted %v or fell back to legacy", v)
		}
	}
	legacy := tracing.Extract(context.Background(), map[string]any{"_traceparent": parent, "_tracestate": "legacy=ok"})
	if !trace.SpanContextFromContext(legacy).IsValid() {
		t.Fatal("legacy not read")
	}
	standard := tracing.Extract(context.Background(), map[string]any{"traceparent": parent, "_tracestate": "legacy=ignored"})
	if trace.SpanContextFromContext(standard).TraceState().Len() != 0 {
		t.Fatal("mixed key tiers")
	}
}

func TestAbsentParamsAndError(t *testing.T) {
	want := errors.New("sensitive error text")
	for _, mw := range []mcpsdk.Middleware{tracing.Sending(), tracing.Receiving(tracing.Options{})} {
		handler := mw(func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) { return nil, want })
		for _, p := range []*mcpsdk.PingParams{nil, {}} {
			_, err := handler(context.Background(), "ping", &mcpsdk.ServerRequest[*mcpsdk.PingParams]{Params: p})
			if !errors.Is(err, want) {
				t.Fatalf("error = %v", err)
			}
		}
	}
}

func TestReceivingEndsErrorSpanWithoutRecordingErrorText(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	want := errors.New("private implementation detail")
	handler := tracing.Receiving(tracing.Options{Tracer: provider.Tracer("test")})(func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) { return nil, want })
	p := &mcpsdk.PingParams{}
	p.SetMeta(map[string]any{"traceparent": parent})
	_, err := handler(context.Background(), "ping", &mcpsdk.ServerRequest[*mcpsdk.PingParams]{Params: p})
	if !errors.Is(err, want) {
		t.Fatal("changed application error")
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Status().Code != codes.Error || spans[0].Status().Description != "MCP request failed" || len(spans[0].Events()) != 0 {
		t.Fatalf("error span = %v", spans)
	}
}
