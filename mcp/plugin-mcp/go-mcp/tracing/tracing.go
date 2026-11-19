// Package tracing provides opt-in W3C trace context propagation in MCP request
// _meta. It configures no exporters, global propagators, or credentials.
package tracing

import (
	"context"
	"maps"
	"reflect"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Inject returns a copy of meta with ctx's valid span context in the standard
// traceparent/tracestate keys. Without a valid span, meta is returned unchanged.
// It never writes tool arguments, identity, baggage, or legacy keys.
func Inject(ctx context.Context, meta map[string]any) map[string]any {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return meta
	}
	out := maps.Clone(meta)
	if out == nil {
		out = make(map[string]any, 2)
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	delete(out, "tracestate")
	delete(out, "_traceparent")
	delete(out, "_tracestate")
	for k, v := range carrier {
		out[k] = v
	}
	return out
}

// Extract reads W3C context from meta, preserving ctx's values and cancellation.
// Legacy underscore-prefixed keys are accepted only if traceparent is absent;
// an invalid standard value cannot fall back to a different legacy parent.
// Other metadata (including identity and baggage) is ignored.
func Extract(ctx context.Context, meta map[string]any) context.Context {
	parent, state := "traceparent", "tracestate"
	if _, exists := meta[parent]; !exists {
		parent, state = "_traceparent", "_tracestate"
	}
	carrier := propagation.MapCarrier{}
	if v, ok := meta[parent].(string); ok {
		carrier["traceparent"] = v
	}
	if v, ok := meta[state].(string); ok {
		carrier["tracestate"] = v
	}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}

// Sending propagates the current span through SDK requests. The request's
// params must be owned by the call, as required for SDK middleware generally.
func Sending() mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if p := requestParams(req); p != nil {
				p.SetMeta(Inject(ctx, p.GetMeta()))
			}
			return next(ctx, method, req)
		}
	}
}

// Options configures receiving spans. The zero value uses the global tracer
// provider's go-mcp tracer, which is a no-op until the application configures it.
type Options struct {
	Tracer trace.Tracer
	// Attributes reads application-owned context only. Supply verified ids from
	// an already-verified run token under distinct attribute names from claimed
	// ids. Never label raw request metadata or plain headers as verified, and
	// never return tokens, credentials, arguments or result content here.
	// It runs only when a span records; it must be cheap and perform no I/O.
	Attributes func(context.Context) []attribute.KeyValue
}

// Receiving extracts the remote parent and starts one server span per SDK
// request. Metadata is never copied into span attributes automatically.
func Receiving(opts Options) mcpsdk.Middleware {
	tracer := opts.Tracer
	if tracer == nil {
		tracer = otel.Tracer("github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp")
	}
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if p := requestParams(req); p != nil {
				ctx = Extract(ctx, p.GetMeta())
			}
			ctx, span := tracer.Start(ctx, method, trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()
			if span.IsRecording() && opts.Attributes != nil {
				span.SetAttributes(opts.Attributes(ctx)...)
			}
			res, err := next(ctx, method, req)
			if err != nil {
				span.SetStatus(codes.Error, "MCP request failed")
			}
			return res, err
		}
	}
}

// SDK Params are pointer values; absent params may be a typed nil interface.
func requestParams(req mcpsdk.Request) mcpsdk.Params {
	p := req.GetParams()
	if p == nil {
		return nil
	}
	v := reflect.ValueOf(p)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return nil
	}
	return p
}
