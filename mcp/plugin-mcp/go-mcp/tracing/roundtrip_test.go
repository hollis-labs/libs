package tracing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/client"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/tracing"
	httptransport "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/transport/http"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestPoolRoundtrip(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer func() { _ = provider.Shutdown(context.Background()) }()
			type verifiedKey struct{}
			attrs := func(ctx context.Context) []attribute.KeyValue {
				id, _ := ctx.Value(verifiedKey{}).(string)
				return []attribute.KeyValue{attribute.String("eval.verified.run_id", id)}
			}
			opts := []server.Option{}
			if enabled {
				opts = append(opts, server.WithTraceContext(tracing.Options{Tracer: provider.Tracer("test"), Attributes: attrs}))
			}
			srv := server.NewServer("trace-test", "test", opts...)
			var mu sync.Mutex
			var got trace.SpanContext
			var meta map[string]any
			srv.RegisterTool(server.Tool{Name: "echo", Description: "Echo", InputSchema: server.EmptyObjectSchema(), ReadOnlyHint: true, Handler: func(ctx context.Context, args map[string]any) (any, error) {
				mu.Lock()
				defer mu.Unlock()
				got = trace.SpanContextFromContext(ctx)
				meta = server.MetaFromContext(ctx)
				if len(args) != 0 {
					t.Error("trace leaked into arguments")
				}
				return map[string]any{"ok": true}, nil
			}})
			handler := httptransport.NewHandler(srv, httptransport.HandlerOptions{})
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Stand-in for authentication that has already verified a run token.
				handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), verifiedKey{}, "trusted-run")))
			}))
			defer httpServer.Close()
			poolOpts := []client.Option{}
			if enabled {
				poolOpts = append(poolOpts, client.WithTraceContext())
			}
			pool := client.NewPool(poolOpts...)
			defer func() { _ = pool.Close() }()
			if err := pool.Register("test", client.ServerConfig{Transport: client.TransportHTTP, URL: httpServer.URL}); err != nil {
				t.Fatal(err)
			}
			ctx := tracing.Extract(context.Background(), map[string]any{"traceparent": parent, "tracestate": "vendor=value"})
			input := map[string]any{"app/key": "keep", "eval.verified.run_id": "forged", "claimed_run_id": "claimed"}
			if _, _, err := pool.CallTool(ctx, "test", "echo", map[string]any{}, client.WithCallMeta(input)); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if enabled {
				if !got.IsValid() || got.TraceID() != trace.SpanContextFromContext(ctx).TraceID() || got.SpanID() == trace.SpanContextFromContext(ctx).SpanID() {
					t.Fatalf("server span = %v", got)
				}
				if meta["traceparent"] != parent || meta["tracestate"] != "vendor=value" {
					t.Fatalf("meta = %v", meta)
				}
				found := false
				for _, span := range recorder.Ended() {
					if span.Name() != "tools/call" {
						continue
					}
					found = true
					if !span.Parent().IsRemote() || span.Parent().SpanID() != trace.SpanContextFromContext(ctx).SpanID() {
						t.Fatalf("parent = %v", span.Parent())
					}
					for _, a := range span.Attributes() {
						if a.Key == "eval.verified.run_id" && a.Value.AsString() != "trusted-run" {
							t.Fatal("forged verified attribution")
						}
					}
					if len(span.Attributes()) != 1 {
						t.Fatalf("unexpected automatic metadata attributes: %v", span.Attributes())
					}
				}
				if !found {
					t.Fatal("no tools/call span")
				}
			} else if got.IsValid() || meta["traceparent"] != nil || len(recorder.Ended()) != 0 {
				t.Fatal("tracing enabled by default")
			}
			if meta["app/key"] != "keep" || input["traceparent"] != nil {
				t.Fatal("lost or mutated user metadata")
			}
		})
	}
}
