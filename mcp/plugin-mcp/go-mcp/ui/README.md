# MCP Apps server transport

MCP Apps is an **optional extension**, including in MCP 2026-07-28. It is
not a new core tool result type. This package implements its server-side
resource and metadata pattern; it does not implement a browser host.

```go
s := server.NewServer("example", "1", server.WithCapabilities(ui.Capabilities()))
err := ui.Register(s, ui.Resource{
    URI: "ui://example/card", Name: "Card", HTML: bundledHTML,
    ConnectDomains: []string{"https://api.example"},
})
// Handle err before registering tools.
tool, err := ui.Attach(myTool, "ui://example/card")
// Handle err, then s.RegisterTool(tool).
```

`Attach` preserves other metadata and sets `_meta.ui.resourceUri`.
`server.Tool.Meta` is also retained by `ToolDefinitions`, catalog fingerprints,
and paginated `tools/list`. Maps nested in metadata are immutable after
registration. `Register` publishes a `ui://` resource containing HTML with MIME
`text/html;profile=mcp-app`; resource content `_meta.ui.csp.connectDomains`
contains only the configured network origins. Other resource policy features
can be implemented directly using the SDK.

`Capabilities` returns fresh `ServerCapabilities` with
`extensions["io.modelcontextprotocol/ui"] = {}`. Client capabilities, in contrast,
declare `mimeTypes: ["text/html;profile=mcp-app"]` under that extension identifier
in each request's `_meta["io.modelcontextprotocol/clientCapabilities"]`. The SDK
provides those per-request settings to `server.ClientCapabilities(ctx)`. When using existing
capabilities, merge that entry into their `Extensions` map instead of replacing
the entire capability set. Use `Supports` with `server.ClientCapabilities(ctx)`
to inspect the caller's explicit MIME support. Hosts without the extension
still receive ordinary structured/text tool results as a fallback. A gateway
must forward upstream resources and tool metadata for apps to work through it.
Host support must be tested independently; this package does not claim every
chat client renders Apps.

The [approval-card example](../examples/approval-card/README.md) bundles a
framework-free app implementing the Apps `2026-01-26` postMessage lifecycle.
That extension protocol version is separate from core MCP's `2026-07-28`.

The extension is opt-in: constructing a normal `server.Server` does not add the
capability, metadata or resources.

Verified 2026-11-19 against the official sources:

- [MCP 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28), Extensions section: explicit opt-in and server/discover capabilities.
- [Extensions overview](https://modelcontextprotocol.io/extensions/overview): distinct client MIME settings and empty server settings, with per-request negotiation.
- [MCP Apps overview](https://modelcontextprotocol.io/extensions/apps/overview): sandbox, resource linkage and host responsibilities.
- [Official Apps specification](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx): resource MIME, nested `_meta.ui`, CSP, lifecycle, capabilities and tool-result notifications.

The package deliberately does not introduce Sampling, Roots, Logging, OAuth,
a general extension framework, or a host credential protocol.
