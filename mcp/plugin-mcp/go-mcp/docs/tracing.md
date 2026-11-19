# Request trace propagation

Opt in independently at each endpoint:

```go
pool := client.NewPool(client.WithTraceContext())
srv := server.NewServer("example", "1.0", server.WithTraceContext(tracing.Options{}))
```

Imports: `client`, `server` and `tracing` under
`github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp`.

The client sends the current span context in every outgoing SDK request's
`params._meta.traceparent` and optional `tracestate`, across HTTP, stdio and legacy
SSE. It creates no client span. The server extracts that context and starts a
server span named for the MCP method. Handlers and downstream calls inherit it.
Direct `Server.CallTool` calls bypass protocol middleware; they use their caller's
context. Protocol errors mark the span with a fixed error description; error text,
arguments, results and tool metadata are not recorded. A tool result's `isError`
flag is application data and is not interpreted by this middleware.

Supply `tracing.Options.Tracer` for a specific tracer provider, or configure the
application's global OTel provider. This feature installs no collector, exporter,
global propagator or background worker. The default provider is a no-op; valid
span contexts still propagate without configuring a global propagator. No option
means no propagation middleware or tracing work. The library adds no baggage
injection/extraction; explicitly supplied metadata remains the caller's responsibility.

A valid current span overrides stale explicit trace keys on the outgoing request;
unrelated metadata is preserved and caller-owned metadata maps are not changed.
Without a valid current span, explicit metadata is preserved unchanged. Extraction
ignores non-string or malformed trace values and preserves deadlines, cancellation
and other context values. It never reads tool arguments. For older peers, inbound
`_traceparent` and `_tracestate` are accepted only if `traceparent` is absent; a
present standard key prevents legacy fallback. New automatic outbound propagation
uses standard keys and removes stale legacy trace keys. Existing `core/util/otel`
helpers are unchanged; applications using those helpers must explicitly adopt
this API to write the standard keys.

## Correlation and attribution

Trace context is untrusted correlation, never authentication. This package does
not parse tokens, inspect identity headers, query a registry, or promote `_meta`
fields to identity. A run-token verifier belongs in the application or gateway.
Install it before the receiving tracing middleware, so it supplies trusted context.
The verifier must validate the token before treating its run/session/task/workstream
and agent ids as verified. Plain id headers and caller metadata remain claimed.

`tracing.Options.Attributes` is an optional cheap callback that reads only context
and returns `[]attribute.KeyValue`. It runs only on recording spans. Applications
may use it to add their already-established verified and claimed correlation
attributes, keeping the namespaces distinct. The callback receives no request
metadata and the library chooses no identity attribute schema. For example:

```go
server.WithTraceContext(tracing.Options{
    Attributes: func(ctx context.Context) []attribute.KeyValue {
        // Application-owned accessor; populated by the token verifier.
        ids := verifiedRunFromContext(ctx)
        return []attribute.KeyValue{
            attribute.String("example.verified.run_id", ids.RunID),
        }
    },
})
```

The accessor and example attribute name above are application-defined. Use the
application's existing correlation vocabulary; do not label plain headers or raw
metadata as verified. Never include a run token, credential, prompt or response
content in attributes, and perform no I/O in the callback. Correlation ids are span
attributes only, not baggage, tool arguments or new wire identity metadata.

## Specification

Checked against the authoritative MCP 2026-07-28
[base protocol, OpenTelemetry trace context](https://modelcontextprotocol.io/specification/2026-07-28/basic/index#meta)
and [changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog).
These reserve `traceparent`, `tracestate` and `baggage` for W3C-format context in
`_meta`. This implementation deliberately carries trace context only. It does not
change the older underscore-key helpers in another package.
