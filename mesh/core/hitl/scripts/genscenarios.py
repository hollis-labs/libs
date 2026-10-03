#!/usr/bin/env python3
"""Regenerate hitltest/scenarios/*.json. Run from the repo root."""
import json

NONTERMINAL = ["submitted", "validated", "staged", "presented", "in_progress"]
TEV = "T:/internal/hitl/service_test.go"

def enq(key="K", app="conf-a", **extra):
    d = {"contract_version": "1.0", "kind": "approval", "idempotency_key": "{{run}}-" + key,
         "source": {"application_id": app, "agent_id": "agent-1"},
         "title": "Approve change", "summary": "s", "request": "r"}
    d.update(extra)
    return d

APPROVE = {"kind": "approval", "decision": "approved"}
DENY = {"kind": "approval", "decision": "denied", "note": "not yet"}
PAST = "2000-01-01T00:00:00Z"

S = []
def sc(name, description, evidence, steps, requires=None):
    S.append({"name": name, "description": description, "tangent_evidence": evidence,
              "requires": requires or [], "steps": steps})

sc("enqueue-idempotent-replay",
   "The same idempotency key with identical content returns the original item and its current state.",
   TEV + ":24 TestEnqueueAllocatesOneGlobalFIFOAndScopesIdempotency",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {"state_in": NONTERMINAL}},
    {"op": "enqueue", "as": "a2", "doc": enq(), "expect": {"same_item_as": "a", "state_in": NONTERMINAL, "revision_same_as": "a"}}])

sc("idempotency-conflict",
   "The same key with different immutable content is an idempotency_conflict naming the existing item and changes nothing.",
   TEV + ":24, :611 TestEnqueueRejectsConcurrentGenericIdempotencyWinner",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "enqueue", "doc": enq(title="A different title"), "expect": {"code": "idempotency_conflict", "existing_item_id_is": "a"}},
    {"op": "get", "item": "a", "expect": {"state_in": NONTERMINAL, "revision_same_as": "a"}}])

sc("idempotency-scope-per-application",
   "Keys are scoped per caller application: the same key from another application creates a distinct item.",
   TEV + ":24 (same key in another application_id)",
   [{"op": "enqueue", "as": "a", "doc": enq(app="conf-a"), "expect": {}},
    {"op": "enqueue", "as": "b", "doc": enq(app="conf-b"), "expect": {"distinct_item_from": "a"}}])

sc("get-pending", "Get returns get/not_waited with a nonterminal item and no outcome.",
   "T:/internal/envelope/extensions/hitl_item_test.go hitlRetrievalFixture",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "get", "item": "a", "expect": {"mode": "get", "wait_status": "not_waited", "state_in": NONTERMINAL, "revision_same_as": "a"}}])

sc("get-terminal", "Get of a terminal item returns its outcome.",
   TEV + ":766 TestWithdrawResolveRaceAndDistinctTerminalCauses",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "withdraw", "item": "a", "as": "w", "fields": {"reason": "done"}, "expect": {"state": "canceled", "outcome_cause": "caller_withdrawn"}},
    {"op": "get", "item": "a", "expect": {"mode": "get", "wait_status": "not_waited", "state": "canceled", "outcome_state": "canceled", "outcome_cause": "caller_withdrawn", "outcome_equals": "w"}}])

sc("await-timeout", "Await with wait_ms 0 (and with a short wait) reports await/timeout and changes no state.",
   TEV + ":658 (await does not mutate)",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "await", "item": "a", "fields": {"wait_ms": 0}, "expect": {"mode": "await", "wait_status": "timeout", "state_in": NONTERMINAL, "revision_same_as": "a"}},
    {"op": "await", "item": "a", "fields": {"wait_ms": 20}, "expect": {"mode": "await", "wait_status": "timeout", "state_in": NONTERMINAL, "revision_same_as": "a"}},
    {"op": "get", "item": "a", "expect": {"state_in": NONTERMINAL, "revision_same_as": "a"}}])

sc("await-terminal", "Await of an already terminal item returns await/terminal with the outcome.",
   TEV + ":658",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "resolve", "item": "a", "as": "r", "response": APPROVE, "expect": {"state": "resolved", "decision": "approved"}},
    {"op": "await", "item": "a", "fields": {"wait_ms": 50}, "expect": {"mode": "await", "wait_status": "terminal", "state": "resolved", "outcome_equals": "r"}}])

sc("await-out-of-range", "wait_ms outside 0..50000 is rejected, not clamped.",
   "T:/internal/hitl/service.go Await (maximumWait check)",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "await", "item": "a", "fields": {"wait_ms": -1}, "expect": {"code": "validation_failed"}},
    {"op": "await", "item": "a", "fields": {"wait_ms": 50001}, "expect": {"code": "validation_failed"}}])

sc("withdraw-then-withdraw", "Withdrawing twice returns the same immutable caller_withdrawn outcome.",
   TEV + ":766",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "withdraw", "item": "a", "as": "w1", "fields": {"reason": "first"}, "expect": {"state": "canceled", "outcome_cause": "caller_withdrawn"}},
    {"op": "withdraw", "item": "a", "as": "w2", "fields": {"reason": "second"}, "expect": {"outcome_equals": "w1"}}])

sc("resolve-then-withdraw", "A withdraw that lost to a resolution is a terminal_conflict carrying the resolved outcome.",
   TEV + ":766",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "resolve", "item": "a", "as": "r", "response": APPROVE, "expect": {"state": "resolved"}},
    {"op": "withdraw", "item": "a", "expect": {"code": "terminal_conflict", "outcome_state": "resolved", "outcome_equals": "r"}}])

sc("withdraw-then-participant-resolve", "A resolve that lost to a withdrawal is refused with an error carrying the canceled outcome.",
   TEV + ":766",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "withdraw", "item": "a", "as": "w", "expect": {"state": "canceled"}},
    {"op": "resolve", "item": "a", "response": APPROVE, "expect": {"code_in": ["terminal_conflict", "stale_revision"], "outcome_state": "canceled", "outcome_equals": "w"}}])

sc("withdraw-stale-expected-revision", "A withdraw whose expected_revision is stale is a stale_revision error and changes nothing.",
   "T:/internal/hitl/service.go Withdraw (staleError)",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "withdraw", "item": "a", "fields": {"expected_revision": 99}, "expect": {"code": "stale_revision"}},
    {"op": "get", "item": "a", "expect": {"state_in": NONTERMINAL, "revision_same_as": "a"}}])

sc("terminal-immutability", "Once terminal, no later operation changes or replaces the outcome.",
   TEV + ":457 (one immutable winner)",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "resolve", "item": "a", "as": "r", "response": APPROVE, "expect": {"state": "resolved", "decision": "approved"}},
    {"op": "resolve", "item": "a", "response": DENY, "expect": {"code": "terminal_conflict", "outcome_equals": "r"}},
    {"op": "withdraw", "item": "a", "expect": {"code": "terminal_conflict", "outcome_equals": "r"}},
    {"op": "get", "item": "a", "expect": {"state": "resolved", "outcome_equals": "r"}},
    {"op": "await", "item": "a", "fields": {"wait_ms": 0}, "expect": {"wait_status": "terminal", "outcome_equals": "r"}}])

sc("caller-isolation", "A caller outside the item's scope gets not_found or unauthorized and cannot affect the item (accident avoidance, not a security boundary).",
   "T:/internal/hitl/service.go inspectHITL; Tangent answers unauthorized within one authority",
   [{"op": "enqueue", "as": "a", "doc": enq(), "expect": {}},
    {"op": "get", "item": "a", "caller": "conf-b", "expect": {"code_in": ["not_found", "unauthorized"]}},
    {"op": "await", "item": "a", "caller": "conf-b", "fields": {"wait_ms": 0}, "expect": {"code_in": ["not_found", "unauthorized"]}},
    {"op": "withdraw", "item": "a", "caller": "conf-b", "expect": {"code_in": ["not_found", "unauthorized"]}},
    {"op": "get", "item": "a", "caller": "conf-a", "expect": {"state_in": NONTERMINAL, "revision_same_as": "a"}}],
   ["caller-isolation"])

sc("expiry-refuses-late-respond", "A respond arriving after expires_at is refused atomically even though no sweeper has run; the expired outcome wins.",
   "none: Tangent has no public expiry path (ExpireInteraction is host-privileged, 0 production callers)",
   [{"op": "enqueue", "as": "a", "doc": enq(expires_at=PAST), "expect": {}},
    {"op": "resolve", "item": "a", "response": APPROVE, "expect": {"code": "terminal_conflict", "outcome_state": "expired"}},
    {"op": "get", "item": "a", "expect": {"state": "expired", "outcome_state": "expired"}}],
   ["expiry-enforcement"])

sc("expiry-refuses-late-withdraw", "A withdraw arriving after expires_at is a terminal_conflict carrying the expired outcome, without a sweeper.",
   "none",
   [{"op": "enqueue", "as": "a", "doc": enq(expires_at=PAST), "expect": {}},
    {"op": "withdraw", "item": "a", "expect": {"code": "terminal_conflict", "outcome_state": "expired"}}],
   ["expiry-enforcement"])

sc("expiry-sweep", "ExpireDue materializes expiry; repeating it changes nothing.",
   "none",
   [{"op": "enqueue", "as": "a", "doc": enq(key="lapsed", expires_at=PAST), "expect": {}},
    {"op": "expire", "expect": {}},
    {"op": "get", "item": "a", "as": "g", "expect": {"state": "expired", "outcome_state": "expired"}},
    {"op": "expire", "expect": {}},
    {"op": "get", "item": "a", "expect": {"state": "expired", "outcome_equals": "g", "revision_same_as": "g"}}],
   ["expiry-enforcement"])

for s in S:
    with open(f"hitltest/scenarios/{s['name']}.json", "w") as f:
        json.dump(s, f, indent=2); f.write("\n")
