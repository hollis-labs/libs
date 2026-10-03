# Vocabulary mapping (informational)

This document is informational. It is **not** normative and is **not** tested against any other application's code. It was assembled from the go-hitl briefs, with `go-envelopes/types.go` and `go-workflow/wait/model.go` skimmed for the names quoted below; no application's HITL implementation was executed. Treat every row as a reading, not a verified equivalence.

## Answered

| System | Spelling | go-hitl |
|---|---|---|
| Tangent | `resolved` | `resolved` |
| Torque `checkpoints.status` | `responded` | `resolved` |
| Hadron (legacy) | `decided` | `resolved` |
| go-workflow `wait` | `resumed` (`StatusResumed`) | `resolved` |
| go-envelopes | `submitted` (`ResponseStatusSubmitted`) | `resolved` |

## Terminal by time (D4)

| System | Spelling | go-hitl |
|---|---|---|
| Tangent | `expired` | `expired` (kept: frozen) |
| Torque | `timed_out` | `expired` |
| go-workflow | `timed_out` (`StatusTimedOut`) | `expired` |
| go-envelopes | `timeout` (`ErrorCodeTimeout`) | `expired` |
| Nanite elicitation | `timeout` (reason) | `expired` |

Default-on-timeout (deny, cancel, fail, block) is caller policy. Torque accepts a late reply until its scheduler sweeps, and its sweep only runs while the scheduler is enabled; that is the negative example for the atomic-refusal requirement in CONTRACT.md section 6.

## Error and cancel

| System | Spelling | go-hitl |
|---|---|---|
| Tangent | `failed` | `failed` |
| go-envelopes | `error` | `failed` |
| go-workflow | no failed wait status | n/a |
| Tangent / Torque / go-workflow | `canceled` | `canceled` (with a cause) |
| go-envelopes | `canceled`; legacy alias `cancelled` (types.go says removal is scheduled for v0.5.0, unreleased) | `canceled` |

go-envelopes also has `partial`, a replaceable non-terminal draft. go-hitl has no partial outcome; drafts are never outcomes.

## Decisions (D7)

Core `decision` is an open string. The profiles use `approved | denied` and `acknowledged`.

| Source | Value | go-hitl |
|---|---|---|
| Torque | `approved`, `rejected`, `needs_info` | `approved`, `denied`, (no equivalent for `needs_info`) |
| Nanite permission | `allow`, `deny` (+ scope once/session) | `approved`, `denied`; scope stays on the request side |
| MCP elicitation | `accept` | `approved` |
| MCP elicitation | `decline` | `denied` |
| MCP elicitation | `cancel` | **not a decision**: maps to `participant_canceled`, never a `resolved` outcome |
| ACP | `selected(allow_*)` | `approved` |
| ACP | `selected(reject_*)` | `denied` |
| ACP | `cancelled` | `canceled` only if human-initiated. ACP forces `cancelled` on every pending request on `session/cancel`; that mechanical case MUST NOT terminalize the interaction (Tangent ADR 0001 section 6: transport cancellation ends a waiter, not the item). |
| Tangent turns | `respond`, `approve`, `reject`, `dismiss` | different vocabulary; not mapped |
| Hadron / go-workflow | arbitrary option ids | carried as the open `decision` string |

MCP elicitation's `accept | decline | cancel` is fixed by MCP.

## `allow` / `deny` / `ask` (go-permission, go-hooks)

Separate vocabulary. Do not merge. `ask` is a policy pre-decision that triggers an interaction; it is never itself a terminal outcome. Recommended mapping for a caller building both: `ask` opens a go-hitl interaction; `approved` becomes `allow`; `denied` becomes `deny`; anything else (`expired`, `canceled`, `failed`) is caller policy, but **fail closed to `deny` for permission gating**. A policy engine's execution-mode modifiers (for example a dry-run mode on `allow`) are orthogonal and do not add a branch to this mapping.

## Responder identity (D3)

go-workflow's `Responder` maps onto `responder{kind, ref}`; its `ResponderAuthorizer`, `Resolution.PayloadDigest` and `Responder.Attributes` seams are where a caller could attach or check a `proof{scheme, key_ref, binds}`. That is a reading of the amendment, not something built or verified here. A workflow gate does not delegate storage to a go-hitl record; it links by correlation id.
