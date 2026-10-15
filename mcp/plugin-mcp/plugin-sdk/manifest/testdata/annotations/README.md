# Tool annotation compatibility fixtures

`without-annotations.json` is the pre-annotation schema-2 shape. The new
SDK must still decode it. `with-annotations.json` covers read, write and
destructive declarations with the MCP keys `readOnlyHint`, `destructiveHint`,
`idempotentHint`, `openWorldHint` and optional `title`.

`older-strict-host-error.txt` freezes the diagnostic observed from the
unchanged decoder at commit 7165988c66cb1d8d960d56ce4307228405494fd7 when
reading the annotations-bearing shape now represented by
`with-annotations.json`: `manifest: unknown field "annotations"`.
It documents version skew; tests do not depend on building or simulating old
SDK code. Hosts using that strict decoder refuse the declaration, rather than
ignoring hints. Use the SDK release carrying annotations before consuming
manifests that include them; no release number is assigned by this change.


`nanite-pre-cutover.json` is an immutable MCP golden corpus for context_pin,
context_unpin and reminder_set. Source: Nanite
`2de304e3d1cebe8d875f7806c03ec0eae8f6b8fe`, the commit built into the
pre-cutover binary with SHA256 prefix `ef20f9be`. The prefix identifies the
binary, not a Git revision. Exact name, description and input schema literals
come from `internal/selftools/self_tools.go`; annotations come from
`internal/mcpserver/annotations.go`, applied unchanged by `buildTool` in
`internal/mcpserver/server.go`. All four booleans are explicit, including
source zero-value false; no title is declared for these tools.

Extraction reads only the pinned Git source and evaluates the copied literals
with a standalone standard-library Go program. It does not execute Nanite or
read, move or modify the protected binary. The test requires no external
checkout: it decodes the frozen corpus, pins its source identity, and validates
a manifest roundtrip with exact MCP annotation projection. The historical
definitions have no effect field; the test supplies an opaque host-defined
value solely to exercise manifest validation.
