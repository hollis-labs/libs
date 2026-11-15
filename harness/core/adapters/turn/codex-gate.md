# Structured Codex approval adapter

`CodexGateResponder` is an opt-in library adapter for `agentsessions` JSON-RPC
stdio. It consumes `mesh/core/gate`, with host-issued runtime references from
`mesh/core/runtimecontract`. It does not install a host, change a launch posture,
provide an identity verifier, or create another interaction store.

The host supplies the existing authoritative GatePort, AuthorityVerifier,
current operation resolver and atomic once-claim port. Register `Respond` as
`StartOptions.JsonRpcRequestResponder` and supply `JsonRpcAttemptBinding` for
every actual process attempt. A replacement must have a fresh attempt ID and a
strictly increasing generation. Preserve that binding through the host's item
registry, policy checks and claim. Provider thread/turn IDs alone are insufficient.

`Resolve` runs before the interaction request and again before a native reply.
Its immutable template includes the host's idempotency key, absolute expiry,
enforcement point, environment, resource versions, policy and obligations.
Its canonical JSON descriptor must include authoritative targets, operation
arguments/plan versions, workspace/environment and effects. The adapter hashes
that descriptor together with the native request and joined command/MCP facts.
Changed operation bytes, choices or runtime binding refuse the old decision.
Raw payloads remain sensitive adapter custody; errors omit their contents.

Set `CodexGateFacts.MaxScope` explicitly to `gate.Once`. This is a host policy
ceiling, not inferred authority from a provider menu. Standard commands retain
the exact `accept`, `decline`, `cancel` native choices. Known remembered choices
(`acceptForSession`, MCP `persist` session/always) are filtered by that ceiling,
never renamed as once grants. The original menu remains in the raw envelope
given to the host resolver and in the operation digest. Unknown choices and
amendment payloads refuse. Session/persistent grant delivery is unsupported.

Supported operation facts:

| Request | Required binding | Native reply |
| --- | --- | --- |
| `item/commandExecution/requestApproval` | Exact thread, turn, item, command and cwd, joined from the trusted item registry when absent on the wire | Exact `decision` value |
| `mcpServer/elicitation/request` | `mode: form`, empty object form, `_meta.codex_approval_kind: mcp_tool_call`, structured `tool_name` and `tool_params`, exact server and trusted current item/arguments | Exact `action`, `{}` content on accept, null on decline/cancel |

The adapter refuses managed network grouping, additional permission subsets,
stdin/other command kinds, policy amendments, generic input forms, URL
elicitations, missing MCP argument metadata, display-only tool names, file
changes and other methods. A future adapter can extend these with their own
binding and fixtures. No canonical authorization is parsed from display text.

Only an authoritative, validated gate resolution can select an option. The
injected verifier resolves responder authority and obligations; an allow also
passes shared `ValidateAdmission` and the host's atomic claim. A denial is
distinct from cancellation. The provider owns its subsequent admission and
effects; the library cannot certify them. The host must fence current binding
and lease through its claim. Unknown claims require reconciliation.

The async runtime keeps at most 64 responder workers, including cancelled
workers that have not returned. Callbacks must honor cancellation. Duplicate
pending IDs or exhausted capacity invalidate the attempt and close its input.
The reader continues during human waits. Resolved/turn/thread cleanup, explicit
RPC cancellation, EOF, Stop and replacement invalidate pending replies.
`serverRequest/resolved` means answered or cleared, not approved or executed.
Native lifecycle cancellation withdraws the gate request with bounded cleanup;
an individual Await timeout alone does not withdraw it.

All writes are serialized. Final ownership checks select the captured writer
for the exact attempt; a late responder cannot write into a successor, even
when native IDs recur. A cancelled blocked write may require closing that
transport input. `JsonRpcReplyObserver` reports `sent` only for a complete write,
`unknown` for failed/partial writes, and `discarded` for fenced replies. These
are delivery observations, never acknowledgements or execution receipts.
Record them in the host's delivery journal and reconcile uncertainty before
retrying. The observer and resolver run on workers and must return promptly.
Notification event callbacks retain their reader-goroutine contract; async
requests are observed by the responder without synthetic request events.

When the new responder is nil, the existing synchronous headless request hook
continues to handle its current consumers. Installing any hook proves only that
emitted requests can be handled. Existing Codex registry mappings remain:

| Posture | Sandbox | Approval policy | Coverage limit |
| --- | --- | --- | --- |
| default | read-only | on-request | Sandbox-local work can avoid callbacks |
| accept-edits | workspace-write | on-request | Workspace-local commands/edits can avoid callbacks |
| plan | read-only | never | Refusal may happen without a callback |
| yolo | danger-full-access | never | Work may execute without a callback |

The fake transport tests establish library behavior, not installed-provider
reachability. No live CLI, native terminal reconciliation, host/UI wiring,
Claude/ACP mapping, or application adoption is claimed.

## Protocol evidence

Reviewed the [official app-server documentation](https://learn.chatgpt.com/docs/app-server)
and pinned `openai/codex` source at
[`5cb38e79842a0e0a6dca5bb97874163e8ee50129`](https://github.com/openai/codex/tree/5cb38e79842a0e0a6dca5bb97874163e8ee50129):

- [command decision grammar](https://github.com/openai/codex/blob/5cb38e79842a0e0a6dca5bb97874163e8ee50129/codex-rs/app-server-protocol/schema/typescript/v2/CommandExecutionApprovalDecision.ts)
  defines the standard choices when `availableDecisions` is absent; an explicit
  menu restricts the offered choices further.
- [command request schema](https://github.com/openai/codex/blob/5cb38e79842a0e0a6dca5bb97874163e8ee50129/codex-rs/app-server-protocol/schema/typescript/v2/CommandExecutionRequestApprovalParams.ts)
  identifies command kind, item routing and optional operation facts.
- [MCP approval construction and parsing](https://github.com/openai/codex/blob/5cb38e79842a0e0a6dca5bb97874163e8ee50129/codex-rs/core/src/mcp_tool_call.rs)
  supplies the empty form and structured tool metadata, and keeps persistence
  separate from the accept action.
- [MCP metadata constants](https://github.com/openai/codex/blob/5cb38e79842a0e0a6dca5bb97874163e8ee50129/codex-rs/protocol/src/mcp_approval_meta.rs)
  identify the privileged discriminator, raw tool parameters and persistence.

Fixtures combine these pinned shapes with synthetic host facts from
`mesh/core/gate/gatetest` and the actual HITL service over its test memory store.
They cover ordinary command and MCP once/deny/cancel, unknown options, incomplete
binding, changed bytes/generation, authority/claim refusal and lifecycle fencing.
