
`host-storage.json`, `host-secrets.json`, `host-egress.json`, and
`host-events-log.json` exercise the seven author helpers through private
request-scoped activation and the directional correlation engine in Go and
TypeScript. Each document supplies an explicit Init offer/grant snapshot;
helpers derive parent/binding/timeout, validate receipts, and reject unavailable
grants before publication. They do not enable reverse RPC in production.
