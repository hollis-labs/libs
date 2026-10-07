# mcp-host

mcp-host is a dual-transport MCP plugin host, as a library: it hosts N
independently addressable "logical MCP servers," each backed by a real
standalone MCP server (`process` mode) or a lightweight plugin-sdk-dialect
subprocess (`inprocess` mode). `apps/station` is the first real consumer —
check it for a worked example before inventing a new one.

## Start Here

- `README.md` covers config format, the process/inprocess split, and the
  package map.
- `mcphost.go` (root package `mcphost`) is the complete "just run this"
  entrypoint — `Run(ctx, cfg, opts)`. Read this first; most new code should
  build on it or the packages it composes, not duplicate its wiring.
- `registry/transport.go` owns the one interface every backing transport
  implements (`ListTools`/`CallTool`) — read this before touching either
  transport package.
- `config/config.go` owns the YAML schema and every validation rule;
  `examples/config/host.yaml` is a complete worked example.

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

There is no CI workflow and no Makefile in this repo, so these are the only
gate (same convention as `go-mcp`).

There is no tracked or ambient `go.work`; both this repo and `apps/station`
now have real tags. Cross-module development against an unreleased change
here is a throwaway `go.work` outside the repos, per the portfolio
convention — never a `replace`, never a tracked workspace file.

## Boundaries

Deliberately decoupled from Nanite's `internal/mcp.Manager`: no uniform
tool-name namespace, no trust tiers, no first-party-builtin concept. Every
logical server keeps its own identity and tool set; this host never merges
them, and an aggregate/mux'd single endpoint across logical servers is
explicitly out of scope, not a missing feature.

No plugin marketplace, no multi-tenancy, no hot-reload from a live config
change — `registry.Registry` supports hot add/remove programmatically, but
nothing above it watches a config file for changes yet.

`transport/process`'s spawn variant does not route through
`go-mcp/client.Pool` — Pool's own stdio dial owns spawning internally and
never exposes the `*exec.Cmd`, which real supervision needs for
`supervise.ClassifyExit`. This library owns the process directly instead
(`mcpsdk.IOTransport` over pipes it controls) and uses Pool only for the
dial (URL) variant. Don't "simplify" the spawn path back onto Pool without
re-solving this.

`transport/inprocess` never sends `mcp/list_tools` to a plugin —
`config.InprocessConfig.Tools` is the tool catalog's only source of truth.
This is deliberate, aligned with Tangent's own plugin host (its manifest
package says outright it "deliberately never calls mcp/list_tools,
because Nanite built runtime self-declaration and discarded it"), not a
workaround. Don't add a live discovery call back in "to keep the manifest
in sync" — that's the exact pattern both this library and Tangent moved
away from on purpose. One consequence worth knowing: it's also the only
reason `examples/plugins/clock-plugin` can use plugin-sdk's own documented
`subprocess.Serve` helper at all — verified against plugin-sdk v0.5.0,
`Serve`'s dispatch has no case for `mcp/list_tools` and no capability
interface for it either, so a plugin that needed live discovery to work
could never have used `Serve` regardless.

A relayed tool with no declared input schema gets `EmptyObjectSchema()`
substituted before registration (`serving/serving.go`) — the official SDK's
`AddTool` panics on a nil schema rather than erroring, and a tool from an
upstream server/plugin that never declared one is a real, not-hypothetical
case.

`inprocess` initialization is trusted programmatic input. A per-spawn
`config.InprocessInitFactory` supplies all three roots, the current host-owned
incarnation, an explicit grant array and expected plugin identity/version.
Missing/invalid input fails before spawn. Every restart needs a fresh generation;
configuration and environment do not create authority. `nil` grants are invalid;
a non-nil empty array is an explicit no-authority choice. This transport remains
forward-only and refuses optional reverse/hooks offers.

`mcphost.Run`'s identity strings (client/server name+version reported over
the wire) are currently hardcoded (`"mcp-host"`/`"dev"` in the transport
packages) rather than configurable. Fine for a single consumer; revisit if
a second consumer wants its own identity reported instead.
