
`host-storage.json`, `host-secrets.json`, `host-egress.json`, and
`host-events-log.json` exercise the seven author helpers through private
request-scoped activation and the directional correlation engine in Go and
TypeScript. Each document supplies an explicit Init offer/grant snapshot;
helpers derive parent/binding/timeout, validate receipts, and reject unavailable
grants before publication. They do not enable reverse RPC in production.

`host-readonly.json`, `host-mcp.json`, and `host-bindings.json` extend the private
helper corpus with resource/server echoes, opaque cursors/tool bindings, tool
outcomes versus authority failures, SDK-owned cancellation references and
same-binding renewal. Correlated reply time anchors verified lease/budget state;
renewal never extends existing calls or forward request deadlines.

`negotiation.json` uses normal Go/TS Serve opt-in and Init offers, checking
acknowledgement, strict rejections and actual callback client availability.
Negotiated child replay and pinned plugin-host interop remain the activation merge gate;
private helper recipes alone do not satisfy it.
