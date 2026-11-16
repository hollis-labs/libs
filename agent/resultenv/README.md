# resultenv

`resultenv/v1` is a neutral tool result contract. The full MCP result remains
inside `mcp_result`; it is never flattened to text or `isError`. The envelope
distinguishes known tool failure, command exit, transport failure, timeout,
cancel, unknown effect, and an operation known not to have run.

For direct command execution, `success` carries `exit_code: 0` and does not
need an MCP result; `nonzero_exit` carries its nonzero code and separate stdout
and stderr. `signal` carries the terminating signal name. `cancel_requested`
and `cancel_confirmed` are separate optional booleans, so a requested cancel
is never mistaken for confirmed termination. A non-MCP `tool_error` carries
`error` rather than a fabricated MCP result.
PTY output uses `combined_output`, never a guessed stdout/stderr split.

Hosts capture raw MCP response JSON with `FromMCPRaw` before their model-facing
projection. They issue a `runtimecontract.Reference` whose SHA-256 digest covers
the exact envelope JSON bytes, then store those bytes in a `Receipt`. `ModelView`
derives a bounded, redacted view and names the receipt reference. It cannot
replace the receipt. Redaction paths are JSON pointers into the envelope.
Receipt JSON transports the exact envelope bytes as base64 in `envelope_bytes`.

`testdata/fixtures.json` is the shared fixture set for Go, tool cards, and CLI
adapters. `schema/resultenv.schema.json` defines the cross-language wire shape.
The result producer supplies `output_schema_ref` and truncation information;
MCP's `CallToolResult` does not itself contain a tool output schema. A host must
reconcile `unknown` before making any new operation. This package never retries.
