#!/usr/bin/env python3
"""Regenerate schema/lifecycle.schema.json from Tangent's frozen bundle.

    python3 scripts/genbundle.py <abs path to tangent request.schema.json>

The 12 COPY defs are taken byte-for-byte (as JSON values) from Tangent's
bundle. Everything else (Core, profile-tagged, NEW) is authored below. The
output is committed; this script exists for provenance and re-generation, and
Tangent's file is only ever read.
"""
import json
import sys

COPY = [
    "SourceAssertionV1", "CallerAssertionV1", "ExternalRefV1",
    "ParticipantCaptureV1", "HITLGetCommandV1", "HITLAwaitCommandV1",
    "HITLWithdrawCommandV1", "CanceledTerminalOutcomeV1",
    "ExpiredTerminalOutcomeV1", "FailedTerminalOutcomeV1",
    "SupersededTerminalOutcomeV1", "HITLIdempotencyConflictErrorV1",
]
PROFILE = ["ApprovalResponseV1", "AttentionResponseV1", "HITLResponseV1"]

STATES = ["submitted", "validated", "staged", "presented", "in_progress",
          "resolved", "canceled", "expired", "failed", "superseded"]
TERMINAL = ["resolved", "canceled", "expired", "failed", "superseded"]
NONTERMINAL = [s for s in STATES if s not in TERMINAL]


def ref(name):
    return {"$ref": "#/$defs/" + name}


def s(minl=1, maxl=None, **kw):
    d = {"type": "string", "minLength": minl}
    if maxl is not None:
        d["maxLength"] = maxl
    d.update(kw)
    return d


CV = {"type": "string", "enum": ["1.0"]}
ITEM_ID = s(1, 256)
REV = {"type": "integer", "minimum": 1}
TS = {"type": "string", "format": "date-time"}
STATE = {"type": "string", "enum": STATES}


def view_rules():
    rules = [{
        "if": {"properties": {"state": {"enum": TERMINAL}}, "required": ["state"]},
        "then": {"required": ["terminal_outcome"]},
        "else": {"properties": {"terminal_outcome": False}},
    }]
    for st, name in [("resolved", "ResolvedTerminalOutcomeCoreV1"),
                     ("canceled", "CanceledTerminalOutcomeV1"),
                     ("expired", "ExpiredTerminalOutcomeV1"),
                     ("failed", "FailedTerminalOutcomeV1"),
                     ("superseded", "SupersededTerminalOutcomeV1")]:
        rules.append({
            "if": {"properties": {"state": {"enum": [st]}}, "required": ["state"]},
            "then": {"properties": {"terminal_outcome": ref(name)}},
        })
    return rules


def retrieval(mode, wait, states=None):
    item = ref("HITLItemViewCoreV1")
    if states:
        item = {"allOf": [item, {"properties": {"state": {"enum": states}}}]}
    return {
        "type": "object",
        "required": ["contract_version", "mode", "wait_status", "retrieved_at", "item"],
        "properties": {
            "contract_version": CV,
            "mode": {"type": "string", "enum": [mode]},
            "wait_status": {"type": "string", "enum": [wait]},
            "retrieved_at": TS,
            "item": item,
        },
    }


def stale(op, kinds):
    return {
        "type": "object",
        "required": ["contract_version", "code", "operation", "item_id",
                     "revision_kind", "expected_revision", "actual_revision",
                     "current_state"],
        "properties": {
            "contract_version": CV,
            "code": {"type": "string", "enum": ["stale_revision"]},
            "operation": {"type": "string", "enum": [op]},
            "item_id": ITEM_ID,
            "revision_kind": {"type": "string", "enum": kinds},
            "expected_revision": REV,
            "actual_revision": REV,
            "current_state": STATE,
            "terminal_outcome": ref("HITLTerminalOutcomeCoreV1"),
        },
    }


def core_defs():
    d = {}
    d["HITLItemHandleCoreV1"] = {
        "type": "object",
        "description": "Core subset of Tangent's HITLItemHandleV1: identity, state, revision. Presentation-specific members an implementation adds are tolerated, never required.",
        "required": ["contract_version", "item_id", "state", "revision"],
        "properties": {"contract_version": CV, "item_id": ITEM_ID, "state": STATE, "revision": REV},
    }
    d["ResponderV1"] = {
        "type": "object",
        "description": "Who answered, as an open (kind, ref) pair. Core does not enumerate kinds.",
        "required": ["kind", "ref"],
        "properties": {"kind": s(1, 64), "ref": s(1, 256)},
        "additionalProperties": False,
        "x-hitl-origin": "go-hitl",
    }
    d["ProofBindV1"] = {
        "type": "object",
        "description": "One thing a proof is cryptographically bound to (for example an approval id, a plan hash, an args digest, an operation, a requester or a nonce). The binding set is defined by the caller.",
        "required": ["name", "value"],
        "properties": {"name": s(1, 128), "value": s(1, 1024)},
        "additionalProperties": False,
        "x-hitl-origin": "go-hitl",
    }
    d["ProofV1"] = {
        "type": "object",
        "description": "Optional, type-tagged evidence that a specific credential vouched for the answer. scheme is an open string; core does not verify proofs or rank them against assurance.",
        "required": ["scheme", "key_ref", "binds"],
        "properties": {
            "scheme": s(1, 64),
            "key_ref": s(1, 512),
            "binds": {"type": "array", "items": ref("ProofBindV1")},
        },
        "additionalProperties": False,
        "x-hitl-origin": "go-hitl",
    }
    d["ParticipantCaptureCoreV1"] = {
        "type": "object",
        "description": "Who answered. Either responder{kind,ref} or Tangent's principal_ref must identify the answerer; assurance is an open string; proof is optional.",
        "properties": {
            "responder": ref("ResponderV1"),
            "principal_ref": s(1, 256),
            "authority": s(1, 128),
            "assurance": s(1, 64),
            "proof": ref("ProofV1"),
        },
        "required": ["assurance"],
        "anyOf": [{"required": ["responder"]}, {"required": ["principal_ref"]}],
    }
    d["HITLResponseCoreV1"] = {
        "type": "object",
        "description": "Core response: a kind and an open-string decision. Extra properties are allowed.",
        "required": ["kind", "decision"],
        "properties": {"kind": s(1, 64), "decision": s(1, 64), "note": s(1), "reply": s(1)},
    }
    d["ResolutionRecordCoreV1"] = {
        "type": "object",
        "required": ["resolution_id", "response", "participant", "resolved_at", "interaction_revision"],
        "properties": {
            "resolution_id": s(1, 256),
            "response": ref("HITLResponseCoreV1"),
            "participant": ref("ParticipantCaptureCoreV1"),
            "resolved_at": TS,
            "interaction_revision": REV,
            "presented_projection_revision": REV,
        },
    }
    d["ResolvedTerminalOutcomeCoreV1"] = {
        "type": "object",
        "required": ["contract_version", "state", "item_id", "interaction_revision", "resolution"],
        "properties": {
            "contract_version": CV,
            "state": {"type": "string", "enum": ["resolved"]},
            "item_id": ITEM_ID,
            "interaction_revision": REV,
            "resolution": ref("ResolutionRecordCoreV1"),
        },
    }
    d["HITLTerminalOutcomeCoreV1"] = {
        "oneOf": [ref("ResolvedTerminalOutcomeCoreV1"), ref("CanceledTerminalOutcomeV1"),
                  ref("ExpiredTerminalOutcomeV1"), ref("FailedTerminalOutcomeV1"),
                  ref("SupersededTerminalOutcomeV1")],
    }
    d["HITLItemViewCoreV1"] = {
        "type": "object",
        "required": ["contract_version", "item_id", "state", "revision",
                     "request_snapshot", "enqueued_at", "updated_at"],
        "properties": {
            "contract_version": CV,
            "item_id": ITEM_ID,
            "state": STATE,
            "revision": REV,
            "request_snapshot": {"type": "object"},
            "enqueued_at": TS,
            "updated_at": TS,
            "terminal_outcome": ref("HITLTerminalOutcomeCoreV1"),
        },
        "allOf": view_rules(),
    }
    d["HITLGetRetrievalResultCoreV1"] = retrieval("get", "not_waited")
    d["HITLAwaitTerminalResultCoreV1"] = retrieval("await", "terminal", TERMINAL)
    d["HITLAwaitTimeoutResultCoreV1"] = retrieval("await", "timeout", NONTERMINAL)
    d["HITLRetrievalResultCoreV1"] = {
        "oneOf": [ref("HITLGetRetrievalResultCoreV1"), ref("HITLAwaitTerminalResultCoreV1"),
                  ref("HITLAwaitTimeoutResultCoreV1")],
    }
    d["HITLResolveStaleRevisionErrorCoreV1"] = stale("resolve", ["interaction", "presented_projection"])
    d["HITLWithdrawStaleRevisionErrorCoreV1"] = stale("withdraw", ["interaction"])
    d["HITLStaleRevisionErrorCoreV1"] = {
        "oneOf": [ref("HITLResolveStaleRevisionErrorCoreV1"), ref("HITLWithdrawStaleRevisionErrorCoreV1")],
    }
    d["HITLEnqueueRequestCoreV1"] = {
        "type": "object",
        "description": "Core enqueue request. Extra properties (title, evidence, ...) are open; profiles and apps define them.",
        "required": ["contract_version", "kind", "idempotency_key", "source"],
        "properties": {
            "contract_version": CV,
            "kind": s(1, 64),
            "idempotency_key": s(1, 200),
            "source": ref("SourceAssertionV1"),
            "expires_at": TS,
            "correlations": {"type": "object"},
        },
    }
    # NEW
    d["HITLTerminalConflictErrorV1"] = {
        "type": "object",
        "description": "A withdraw or resolve lost to another terminal outcome; carries the existing immutable outcome. Emitted by Tangent's hitlError but never declared in its error.schema.json.",
        "required": ["contract_version", "code", "terminal_outcome"],
        "properties": {
            "contract_version": CV,
            "code": {"type": "string", "enum": ["terminal_conflict"]},
            "message": s(1, 2000),
            "terminal_outcome": ref("HITLTerminalOutcomeCoreV1"),
        },
        "x-hitl-origin": "go-hitl",
    }
    d["HITLErrorV1"] = {
        "oneOf": [ref("HITLStaleRevisionErrorCoreV1"), ref("HITLIdempotencyConflictErrorV1"),
                  ref("HITLTerminalConflictErrorV1")],
        "x-hitl-origin": "go-hitl",
    }
    d["HITLPlainErrorV1"] = {
        "type": "object",
        "description": "Any other failure: a code and a message, no typed payload. Codes seen from Tangent's hitlError: hitl_error, not_found, unauthorized, validation_failed, wait_canceled, await_timeout.",
        "required": ["contract_version", "code"],
        "properties": {
            "contract_version": CV,
            "code": s(1, 64, not_={"enum": ["stale_revision", "idempotency_conflict", "terminal_conflict"]}),
            "message": s(1, 4000),
        },
        "x-hitl-origin": "go-hitl",
    }
    return d


def main():
    src = json.load(open(sys.argv[1]))["$defs"]
    defs = {}
    for name in COPY:
        defs[name] = src[name]
    for name in PROFILE:
        v = dict(src[name])
        v["x-hitl-tier"] = "profile"
        defs[name] = v
    defs.update(core_defs())
    # fix python-keyword workaround
    for v in defs.values():
        if isinstance(v, dict):
            props = v.get("properties", {})
            for p in props.values():
                if isinstance(p, dict) and "not_" in p:
                    p["not"] = p.pop("not_")
    bundle = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": "https://schemas.hollis-labs.dev/go-hitl/lifecycle/1.0/schema.json",
        "title": "go-hitl lifecycle contract 1.0",
        "description": "Kind-agnostic HITL lifecycle definitions. Validate against one definition at a time (schema.NewValidator); there is no root validation. Derived from Tangent's tangent.hitl-item v1.0 bundle.",
        "$defs": dict(sorted(defs.items())),
    }
    with open("schema/lifecycle.schema.json", "w") as f:
        json.dump(bundle, f, indent=2, sort_keys=False)
        f.write("\n")


main()
