# resultenv

`resultenv/v1` is a neutral tool result contract. The full MCP result remains
inside `mcp_result`; it is never flattened to text or `isError`. The envelope
distinguishes known tool failure, command exit, transport failure, timeout,
cancel, unknown effect, and an operation known not to have run.

Hosts capture raw MCP response JSON with `FromMCPRaw` before their model-facing
projection. They issue a `runtimecontract.Reference` whose SHA-256 digest covers
the exact envelope JSON bytes, then store those bytes in a `Receipt`. `ModelView`
derives a bounded, redacted view and names the receipt reference. It cannot
replace the receipt. Redaction paths are JSON pointers into the envelope.

`testdata/fixtures.json` is the shared fixture set for Go, tool cards, and CLI
adapters. `schema/resultenv.schema.json` defines the cross-language wire shape.
The result producer supplies `output_schema_ref` and truncation information;
MCP's `CallToolResult` does not itself contain a tool output schema. A host must
reconcile `unknown` before making any new operation. This package never retries.
