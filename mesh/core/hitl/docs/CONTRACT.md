# go-hitl contract (wire `contract_version` 1.0)

Normative prose for the lifecycle. The machine-readable form is `schema/lifecycle.schema.json`; where the two disagree the schema wins. This contract is derived from Tangent's `tangent.hitl-item` v1.0 contract and ADR 0001 section 6, kept where they are generic and relaxed where they are presentation-bound. Words MUST, MUST NOT and MAY are used in the RFC 2119 sense.

## 1. Model

One interaction is one *item*: an immutable request, a monotonically increasing integer *revision* (from 1), a *state*, and, once terminal, exactly one immutable *outcome*. The asker holds a *handle* (`item_id`, `state`, `revision`). The contract is kind-agnostic: `kind` is an open string. `approval` and `attention` are the *profiles* this bundle names (tagged `x-hitl-tier: profile`); nothing in core depends on them.

Two version numbers exist and MUST NOT be conflated: the wire `contract_version` (`"1.0"`, on every command and result) and a definition version (Tangent's manifest says `"1.1"`). This bundle uses `"1.0"` on the wire only.

## 2. States and transitions

States: `submitted validated staged presented in_progress resolved canceled expired failed superseded`. The spelling is `in_progress` (ADR 0001 writes `in-progress`; the schema wins). The last five are terminal.

```
submitted -> validated -> staged -> presented -> in_progress
presented | in_progress -> resolved
submitted | validated | staged | presented | in_progress -> canceled | expired
submitted | validated | staged -> failed
staged | presented | in_progress -> superseded
```

A terminal state MUST NOT be left and its outcome MUST NOT change or be replaced. `CanTransition` encodes this table. Only `presented` and `in_progress` items are respondable.

## 3. Verbs

| Verb | Command | Success | Mutates |
|---|---|---|---|
| enqueue | `HITLEnqueueRequestCoreV1` | `HITLItemHandleCoreV1` | creates |
| get | `HITLGetCommandV1` | `HITLGetRetrievalResultCoreV1` | never |
| await | `HITLAwaitCommandV1` | `HITLRetrievalResultCoreV1` | never |
| withdraw | `HITLWithdrawCommandV1` | `HITLTerminalOutcomeCoreV1` | to `canceled` |

Participant resolution is **not** a wire verb (it is presentation-bound). Only its result, a `resolved` outcome, and its `operation: "resolve"` stale error are specified. There is no `subscribe` and no `resume`; those are consumer-side.

Commands are strict: unknown members are rejected. Outputs are tolerant: consumers MUST ignore unknown members.

### Enqueue and idempotency

Uniqueness is `(caller scope, idempotency_key)`, where the caller scope is `source.application_id`. The same key with the same content returns the original item and its *current* state. The digest covers every request member except `idempotency_key`, over canonical JSON (object keys sorted, array order kept). The same key with different content is an `idempotency_conflict` naming `existing_item_id` and changes nothing. Keys from different applications do not collide. `expires_at` is optional and absolute (RFC 3339).

### Get and Await

Both require `caller.application_id` and return `not_found` (or `unauthorized`) to a caller outside the item's scope. Scopes are advisory partitions that prevent accidents; a caller can assert any application id, so this is **not** a security boundary and the contract makes no isolation guarantee beyond that.

`wait_ms` is 0..50000, default 30000; values outside the range are rejected (`validation_failed`), never clamped. The result is exactly one of three rows: `get/not_waited` (any item), `await/terminal` (item terminal), `await/timeout` (item nonterminal). Ending an await, by timeout or by the caller going away, changes nothing about the item.

### Withdraw

Withdraw is the caller-facing cancel; its cause is fixed at `caller_withdrawn`. Repeating it after a `caller_withdrawn` cancel returns the same outcome. If any other terminal outcome exists, withdraw returns `terminal_conflict` carrying that outcome. `expected_revision`, when given and different from the item's revision, is a `stale_revision` error (`revision_kind: "interaction"` only).

## 4. Outcomes

Exactly five, discriminated by `state`; each has `item_id`, `interaction_revision` and (except `resolved`, which has `resolution.resolved_at`) `terminated_at`:

- `resolved`: `resolution{resolution_id, response, participant, resolved_at, interaction_revision, presented_projection_revision?}`. `presented_projection_revision` is optional because only Tangent has a presentation revision.
- `canceled`: `cause` in `caller_withdrawn | caller_canceled | participant_canceled | administrator_canceled | surface_policy`, optional `reason`.
- `expired`: optional `policy_ref` (Tangent writes `request.expires_at`).
- `failed`: `error_code`, `message`.
- `superseded`: `replacement_item_id`.

There is no `partial` outcome: drafts are never outcomes.

## 5. First terminal wins

Any two operations that would each end an item (respond, withdraw, expiry) race; exactly one wins by compare-and-set, and the loser receives an error carrying the existing outcome: `terminal_conflict`, or `stale_revision` with `terminal_outcome` when it supplied a stale expected revision. A lost response is recovered with get or await, never by reapplying the action.

Errors on the wire are `{"contract_version","code",...}`: `stale_revision`, `idempotency_conflict`, `terminal_conflict` (declared as `HITLErrorV1`, of which `terminal_conflict` is new to this bundle; Tangent already emits it but its own error schema never declared it), and plain `{code, message}` for `not_found`, `unauthorized`, `validation_failed`, `wait_canceled`, `await_timeout`, `hitl_error` (`HITLPlainErrorV1`).

## 6. Expiry (D4)

`expired` is the spelling of "the deadline passed" (`timed_out`, `timeout` are other systems' spellings; see MAPPING.md). Timeout ownership is the record owner's, not the transport's.

- An implementation MUST document whether it enforces `expires_at`. Enforcement is the optional capability `expiry-enforcement`.
- **If it enforces**, a respond or withdraw that arrives at or after `expires_at` MUST be refused atomically, even if no sweeper has run: the item becomes `expired` and the caller receives the conflict carrying the expired outcome. A late reply MUST NOT be accepted until a sweep happens to notice. (Torque's behavior of accepting a late reply until its scheduler sweeps is the negative example.) The boundary instant belongs to expiry: `now >= expires_at` is late.
- **Get and Await never mutate, expiry included.** Past `expires_at` a read MUST report the logically correct expired state and outcome, computed at read time, so a caller never sees a stale pending item; it MUST NOT persist that (no store write, no revision bump, no event). Only writes materialize expiry: a late respond or withdraw, or a sweep (`ExpireDue` in the reference `Service`). The computed view is what the write later persists (revision+1, `terminated_at` = `expires_at`), so materializing it later changes nothing the reader saw. An await whose deadline passes while waiting returns the computed expired result.
- Default-on-timeout (deny, cancel, fail, block) is caller policy and not part of this contract. Who runs the sweeper is not part of this contract. Whether an unenforced `expires_at` must be a validation error is not decided; an implementation that cannot enforce a deadline SHOULD reject it rather than pretend to honor it.
- A transport deadline, an await timeout, a dropped connection or a process shutdown does not expire, withdraw or cancel an item.

## 7. Responder identity (D3)

`participant` records who answered:

- `responder{kind, ref}`: open strings; core enumerates no kinds. Tangent's `principal_ref`/`authority` spelling is also accepted; at least one of `responder` or `principal_ref` is required.
- `assurance`: an **open string**. Tangent's frozen enum is `loopback-unverified | asserted | authenticated`; ADR 0004 additionally names `adapter-verified`, which Tangent's enum lacks. **Open question, not decided here:** whether that fourth value becomes part of a closed set. Core treats the field as open.
- `proof{scheme, key_ref, binds}` (optional): type-tagged evidence that a specific credential vouched for the answer. `scheme` is an open string (for example `webauthn`); `key_ref` identifies the credential; `binds` is a list of `{name, value}` pairs naming what the proof is cryptographically bound to (for example an approval id, a plan hash, an args digest, an operation, a requester, a nonce). The binding set is caller-defined; core only carries it.

Core does not verify a proof, does not enumerate schemes, and does not order a present `proof` against `assurance`. A policy such as "require `proof.scheme` before honoring `approved` for a destructive action" belongs to the caller. `ParticipantCaptureV1` (Tangent's shape, closed enum) is retained unchanged for compatibility; `ParticipantCaptureCoreV1` is what `ResolutionRecordCoreV1` uses.

## 8. Decision vocabulary (D7)

`response` is `{kind, decision, ...}`. In core both are open strings. The profiles fix them: `approval` takes `approved | denied` (optional `note`), `attention` takes `acknowledged` (optional `note`, `reply`). Grant scope belongs on the request side, never in the human's answer. Other systems' vocabularies appear only in MAPPING.md. `allow`/`deny`/`ask` is a separate vocabulary and is not merged with this one.

## 9. Not in this contract

Delivery and acknowledgement, drafts, surfaces and queues, evidence blocks, retention, quorum, escalation, and a `Checkpoint*` name for anything (the word has six meanings in the portfolio and is not a spec term). Retention is a host matter: Tangent redacts terminal payloads 30 days after the terminal state by default; nothing here encodes that.
