# plugin-mcp

Plugin and MCP libraries in one Go module, with the history of the six original repositories preserved.

| Package root | Purpose |
| --- | --- |
| `plugin-sdk` | Plugin interfaces, capability contracts and subprocess protocol. |
| `plugin-host` | Host-owned subprocess lifecycle and transport. |
| `mcp-host` | Configured logical MCP servers and process/plugin transports. |
| `go-mcp` | MCP clients, servers, relay, tooling and supervision helpers. |
| `go-mcp-sanitize` | MCP input and output sanitization. |
| `api-projection` | API descriptions projected into tool definitions. |

The Go module is `github.com/hollis-labs/libs/plugin-mcp`. For example, the SDK subprocess package is `github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess` and the host package is `github.com/hollis-labs/libs/plugin-mcp/plugin-host`. One module release versions all six package trees. Its tag has the form `plugin-mcp/vX.Y.Z`; separate SDK or host tags are not needed to consume these packages.

The SDK TypeScript workspaces remain under `plugin-sdk/ts`. Their npm package names are unchanged. Plugin hooks stay in their independent repository and are not included in this module.

Read each package's README and changelog for its contracts. A source release does not establish application adoption, native runtime conformance, security policy or live deployment. The module root has no runtime API.

## Development

Set `GOWORK=off`. From the repository root, `scripts/check plugin-mcp` checks formatting, vet, build and tests; `scripts/check-layout` and `scripts/check-one-way` check the repository rules. Each package retains its protocol fixtures and focused tests. TypeScript checks run from `plugin-sdk/ts` using its lockfile.

MIT; see [LICENSE](LICENSE). Imported package trees retain their original license files as well.
