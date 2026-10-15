#!/usr/bin/env python3
"""Regenerate hitltest/testdata/{valid,invalid}. Run from the repo root."""
import json, os
root = "hitltest/testdata"
T = "2026-09-04T03:30:00Z"
def w(kind, name, d, doc, why=None):
    e = {"def": d, "name": name}
    if why is not None: e["rejects_because"] = why
    e["doc"] = doc
    with open(f"{root}/{kind}/{name}.json", "w") as f:
        json.dump(e, f, indent=2); f.write("\n")

src = {"application_id": "codex", "agent_id": "msg://agent/codex/worker-7"}
caller = {"application_id": "codex"}
part_t = {"principal_ref": "local-operator", "authority": "tangent-loopback", "assurance": "loopback-unverified"}
part_r = {"responder": {"kind": "human", "ref": "operator@example.test"}, "assurance": "asserted"}
proof = {"scheme": "webauthn", "key_ref": "cred:3f9a", "binds": [
    {"name": "approval_id", "value": "appr_01"}, {"name": "plan_hash", "value": "sha256:ab12"},
    {"name": "operation", "value": "deploy"}, {"name": "nonce", "value": "n-77"}]}
part_p = {"responder": {"kind": "human", "ref": "operator@example.test"}, "assurance": "authenticated", "proof": proof}
def res(part, extra=None):
    r = {"resolution_id": "resolution_01", "response": {"kind": "approval", "decision": "approved"},
         "participant": part, "resolved_at": T, "interaction_revision": 5}
    if extra: r.update(extra)
    return r
def resolved(part, extra=None):
    return {"contract_version": "1.0", "state": "resolved", "item_id": "item_01", "interaction_revision": 5, "resolution": res(part, extra)}
canceled = {"contract_version": "1.0", "state": "canceled", "item_id": "item_02", "interaction_revision": 3, "cause": "caller_withdrawn", "reason": "superseded by a newer plan", "terminated_at": T}
expired = {"contract_version": "1.0", "state": "expired", "item_id": "item_03", "interaction_revision": 4, "policy_ref": "request.expires_at", "terminated_at": T}
failed = {"contract_version": "1.0", "state": "failed", "item_id": "item_04", "interaction_revision": 2, "error_code": "materialization_failed", "message": "could not stage", "terminated_at": T}
sup = {"contract_version": "1.0", "state": "superseded", "item_id": "item_05", "interaction_revision": 3, "replacement_item_id": "item_06", "terminated_at": T}
req = {"contract_version": "1.0", "kind": "approval", "idempotency_key": "k-1", "source": src}
def view(state, rev, outcome=None, **kw):
    v = {"contract_version": "1.0", "item_id": "item_01", "state": state, "revision": rev,
         "request_snapshot": req, "enqueued_at": "2026-09-04T03:28:00Z", "updated_at": T}
    if outcome is not None: v["terminal_outcome"] = outcome
    v.update(kw); return v
def retr(mode, wait, item):
    return {"contract_version": "1.0", "mode": mode, "wait_status": wait, "retrieved_at": T, "item": item}
def stale(op, kind, outcome=None, cur="presented"):
    d = {"contract_version": "1.0", "code": "stale_revision", "operation": op, "item_id": "item_01",
         "revision_kind": kind, "expected_revision": 2, "actual_revision": 3, "current_state": cur}
    if outcome: d["terminal_outcome"] = outcome
    return d

V = lambda name, d, doc: w("valid", name, d, doc)
I = lambda name, d, doc, why: w("invalid", name, d, doc, why)

V("source-min", "SourceAssertionV1", src)
V("caller-min", "CallerAssertionV1", caller)
V("caller-principal", "CallerAssertionV1", {"application_id": "codex", "principal_ref": "worker-9"})
V("external-ref", "ExternalRefV1", {"authority": "torque", "id": "CW-1", "revision": "4", "label": "task"})
V("participant-tangent", "ParticipantCaptureV1", part_t)
V("participant-core-tangent-shape", "ParticipantCaptureCoreV1", part_t)
V("participant-core-responder", "ParticipantCaptureCoreV1", part_r)
V("participant-core-proof", "ParticipantCaptureCoreV1", part_p)
V("participant-core-open-assurance", "ParticipantCaptureCoreV1", {"responder": {"kind": "service", "ref": "svc-1"}, "assurance": "adapter-verified"})
V("responder", "ResponderV1", {"kind": "human", "ref": "operator@example.test"})
V("proof-webauthn", "ProofV1", proof)
V("proof-empty-binds", "ProofV1", {"scheme": "webauthn", "key_ref": "cred:1", "binds": []})
V("proof-other-scheme", "ProofV1", {"scheme": "x509", "key_ref": "cn=ops", "binds": [{"name": "nonce", "value": "1"}]})
V("get-command", "HITLGetCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller})
V("await-default-wait", "HITLAwaitCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller})
V("await-wait-zero", "HITLAwaitCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller, "wait_ms": 0})
V("await-wait-max", "HITLAwaitCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller, "wait_ms": 50000})
V("withdraw-min", "HITLWithdrawCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller})
V("withdraw-full", "HITLWithdrawCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller, "expected_revision": 2, "reason": "no longer needed"})
V("response-approval", "ApprovalResponseV1", {"kind": "approval", "decision": "approved"})
V("response-approval-denied-note", "HITLResponseV1", {"kind": "approval", "decision": "denied", "note": "add a rollback test"})
V("response-attention", "AttentionResponseV1", {"kind": "attention", "decision": "acknowledged", "note": "n", "reply": "r"})
V("response-core-open-decision", "HITLResponseCoreV1", {"kind": "choice", "decision": "option-b", "extra_member": 1})
V("resolution-core-no-projection", "ResolutionRecordCoreV1", res(part_r))
V("resolution-core-with-projection", "ResolutionRecordCoreV1", res(part_t, {"presented_projection_revision": 3}))
V("outcome-resolved-tangent", "ResolvedTerminalOutcomeCoreV1", resolved(part_t, {"presented_projection_revision": 3}))
V("outcome-resolved-proof", "HITLTerminalOutcomeCoreV1", resolved(part_p))
V("outcome-canceled", "HITLTerminalOutcomeCoreV1", canceled)
V("outcome-canceled-min", "CanceledTerminalOutcomeV1", {k: v for k, v in canceled.items() if k != "reason"})
V("outcome-expired", "HITLTerminalOutcomeCoreV1", expired)
V("outcome-failed", "HITLTerminalOutcomeCoreV1", failed)
V("outcome-superseded", "HITLTerminalOutcomeCoreV1", sup)
V("handle-min", "HITLItemHandleCoreV1", {"contract_version": "1.0", "item_id": "item_01", "state": "staged", "revision": 3})
V("handle-tangent-extras", "HITLItemHandleCoreV1", {"contract_version": "1.0", "surface_id": "s", "item_id": "item_01", "state": "resolved", "revision": 5, "queue_sequence": 42, "queue_position": None, "inbox_url": "/hitl", "item_url": "/hitl/items/item_01"})
V("view-presented", "HITLItemViewCoreV1", view("presented", 4))
V("view-resolved", "HITLItemViewCoreV1", view("resolved", 5, resolved(part_r)))
V("view-canceled", "HITLItemViewCoreV1", view("canceled", 3, canceled))
V("view-expired", "HITLItemViewCoreV1", view("expired", 4, expired))
V("view-failed", "HITLItemViewCoreV1", view("failed", 2, failed))
V("view-superseded", "HITLItemViewCoreV1", view("superseded", 3, sup))
V("retrieval-get", "HITLRetrievalResultCoreV1", retr("get", "not_waited", view("presented", 4)))
V("retrieval-get-terminal", "HITLGetRetrievalResultCoreV1", retr("get", "not_waited", view("canceled", 3, canceled)))
V("retrieval-await-terminal", "HITLRetrievalResultCoreV1", retr("await", "terminal", view("resolved", 5, resolved(part_r))))
V("retrieval-await-timeout", "HITLRetrievalResultCoreV1", retr("await", "timeout", view("presented", 4)))
V("stale-resolve-interaction", "HITLStaleRevisionErrorCoreV1", stale("resolve", "interaction"))
V("stale-resolve-projection", "HITLResolveStaleRevisionErrorCoreV1", stale("resolve", "presented_projection"))
V("stale-withdraw-terminal", "HITLStaleRevisionErrorCoreV1", stale("withdraw", "interaction", canceled, "canceled"))
V("idempotency-conflict", "HITLIdempotencyConflictErrorV1", {"contract_version": "1.0", "code": "idempotency_conflict", "idempotency_key": "k-1", "existing_item_id": "item_01"})
V("terminal-conflict", "HITLTerminalConflictErrorV1", {"contract_version": "1.0", "code": "terminal_conflict", "message": "item is already resolved", "terminal_outcome": resolved(part_r)})
V("error-union-terminal-conflict", "HITLErrorV1", {"contract_version": "1.0", "code": "terminal_conflict", "terminal_outcome": canceled})
V("error-union-stale", "HITLErrorV1", stale("withdraw", "interaction"))
V("error-plain-not-found", "HITLPlainErrorV1", {"contract_version": "1.0", "code": "not_found", "message": "hitl: not found"})
V("enqueue-min", "HITLEnqueueRequestCoreV1", req)
V("enqueue-tangent-full", "HITLEnqueueRequestCoreV1", {**req, "title": "t", "summary": "s", "request": "r", "expires_at": "2026-12-01T00:00:00Z", "correlations": {"task": {"authority": "torque", "id": "CW-1"}}, "evidence": []})
V("enqueue-custom-kind", "HITLEnqueueRequestCoreV1", {**req, "kind": "choice"})

I("await-wait-over-max", "HITLAwaitCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller, "wait_ms": 50001}, "/wait_ms")
I("await-wait-negative", "HITLAwaitCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller, "wait_ms": -1}, "/wait_ms")
I("get-missing-caller", "HITLGetCommandV1", {"contract_version": "1.0", "item_id": "item_01"}, "")
I("get-wrong-version", "HITLGetCommandV1", {"contract_version": "2.0", "item_id": "item_01", "caller": caller}, "/contract_version")
I("get-unknown-field", "HITLGetCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller, "surprise": True}, "")
I("withdraw-revision-zero", "HITLWithdrawCommandV1", {"contract_version": "1.0", "item_id": "item_01", "caller": caller, "expected_revision": 0}, "/expected_revision")
I("caller-empty-app", "CallerAssertionV1", {"application_id": ""}, "/application_id")
I("outcome-canceled-bad-cause", "CanceledTerminalOutcomeV1", {**canceled, "cause": "policy"}, "/cause")
I("outcome-failed-no-message", "FailedTerminalOutcomeV1", {k: v for k, v in failed.items() if k != "message"}, "")
I("outcome-superseded-no-replacement", "SupersededTerminalOutcomeV1", {k: v for k, v in sup.items() if k != "replacement_item_id"}, "")
I("outcome-resolved-no-resolution", "ResolvedTerminalOutcomeCoreV1", {k: v for k, v in resolved(part_r).items() if k != "resolution"}, "")
I("outcome-expired-zero-revision", "ExpiredTerminalOutcomeV1", {**expired, "interaction_revision": 0}, "/interaction_revision")
I("proof-no-binds", "ProofV1", {"scheme": "webauthn", "key_ref": "cred:1"}, "")
I("proof-bind-no-value", "ProofV1", {"scheme": "webauthn", "key_ref": "cred:1", "binds": [{"name": "nonce"}]}, "/binds/0")
I("proof-unknown-field", "ProofV1", {**proof, "signature": "AAAA"}, "")
I("participant-no-identity", "ParticipantCaptureCoreV1", {"assurance": "asserted"}, "")
I("participant-no-assurance", "ParticipantCaptureCoreV1", {"responder": {"kind": "human", "ref": "x"}}, "")
I("participant-tangent-open-assurance", "ParticipantCaptureV1", {**part_t, "assurance": "adapter-verified"}, "/assurance")
I("responder-no-ref", "ResponderV1", {"kind": "human"}, "")
I("resolution-projection-zero", "ResolutionRecordCoreV1", res(part_r, {"presented_projection_revision": 0}), "/presented_projection_revision")
I("response-core-no-decision", "HITLResponseCoreV1", {"kind": "approval"}, "")
I("response-approval-bad-decision", "ApprovalResponseV1", {"kind": "approval", "decision": "maybe"}, "/decision")
I("response-approval-blank-note", "ApprovalResponseV1", {"kind": "approval", "decision": "approved", "note": ""}, "/note")
I("handle-bad-state", "HITLItemHandleCoreV1", {"contract_version": "1.0", "item_id": "item_01", "state": "closed", "revision": 3}, "/state")
I("handle-no-revision", "HITLItemHandleCoreV1", {"contract_version": "1.0", "item_id": "item_01", "state": "staged"}, "")
I("view-terminal-no-outcome", "HITLItemViewCoreV1", view("resolved", 5), "")
I("view-nonterminal-with-outcome", "HITLItemViewCoreV1", view("presented", 4, canceled), "/terminal_outcome")
I("view-outcome-state-mismatch", "HITLItemViewCoreV1", view("resolved", 5, canceled), "/terminal_outcome/state")
I("view-snapshot-not-object", "HITLItemViewCoreV1", view("presented", 4, request_snapshot="x"), "/request_snapshot")
I("retrieval-get-with-await-status", "HITLRetrievalResultCoreV1", retr("get", "terminal", view("resolved", 5, resolved(part_r))), "/wait_status")
I("retrieval-await-terminal-nonterminal-item", "HITLAwaitTerminalResultCoreV1", retr("await", "terminal", view("presented", 4)), "/item/state")
I("retrieval-await-timeout-terminal-item", "HITLAwaitTimeoutResultCoreV1", retr("await", "timeout", view("canceled", 3, canceled)), "/item/state")
I("stale-withdraw-projection-kind", "HITLWithdrawStaleRevisionErrorCoreV1", stale("withdraw", "presented_projection"), "/revision_kind")
I("stale-wrong-operation", "HITLResolveStaleRevisionErrorCoreV1", stale("withdraw", "interaction"), "/operation")
I("idempotency-conflict-no-existing", "HITLIdempotencyConflictErrorV1", {"contract_version": "1.0", "code": "idempotency_conflict", "idempotency_key": "k"}, "")
I("terminal-conflict-no-outcome", "HITLTerminalConflictErrorV1", {"contract_version": "1.0", "code": "terminal_conflict"}, "")
I("terminal-conflict-bad-outcome", "HITLTerminalConflictErrorV1", {"contract_version": "1.0", "code": "terminal_conflict", "terminal_outcome": {"state": "presented"}}, "/terminal_outcome")
I("error-plain-typed-code", "HITLPlainErrorV1", {"contract_version": "1.0", "code": "stale_revision"}, "/code")
I("enqueue-key-too-long", "HITLEnqueueRequestCoreV1", {**req, "idempotency_key": "k" * 201}, "/idempotency_key")
I("enqueue-bad-expiry", "HITLEnqueueRequestCoreV1", {**req, "expires_at": "tomorrow"}, "/expires_at")
I("enqueue-no-source", "HITLEnqueueRequestCoreV1", {k: v for k, v in req.items() if k != "source"}, "")
I("enqueue-source-no-agent", "HITLEnqueueRequestCoreV1", {**req, "source": {"application_id": "codex"}}, "/source")
I("enqueue-correlations-array", "HITLEnqueueRequestCoreV1", {**req, "correlations": []}, "/correlations")
