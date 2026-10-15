# Policy

`github.com/hollis-labs/substrate/policy` provides a neutral policy boundary:
host-projected requests, `allow` / `ask` / `deny` decisions, mandatory obligations,
a swappable PDP, one evaluation audit event and explicit per-call failure policy.
The module uses only the standard library.

The host supplies **already verified** principal and delegation references,
resolved resource metadata and policy facts. `Request.Validate` checks shape;
it does not authenticate an identity, validate a policy schema or establish a
grant. Context may contain private facts and never appears in the audit event.
The host must supply safe audit metadata in identity, action, resource, reason,
policy provenance and obligation references.

## Evaluation and enforcement

Implement `PDP.Evaluate(context.Context, Request) (Decision, error)` and an
`AuditSink.Record(context.Context, AuditEvent) error`, then call
`Evaluator.Evaluate` with explicit `Options`. The host should bound the context
for both calls. The evaluator has no internal queue, timeout goroutine or counter.

| Situation | Returned admission |
| --- | --- |
| Valid decision, `Enforce` | Original allow, ask or deny |
| Valid decision, `Shadow` | Allow at this boundary; original decision retained |
| PDP missing, error or invalid decision; `FailClosed` | Deny, with classified error |
| Same failure; `FailOpenWithAudit` | Allow only after successful audit, with classified error retained |
| Invalid request/options or canceled call | Deny regardless of mode/failure policy |
| Missing or failed audit sink | Deny with `ErrAuditUnavailable` in every mode |

`Result.Decision` is evaluated intent; `Admission` is this boundary's projection.
`WouldBlock` describes an ask or deny, not a rate counter or fulfillment result.
Shadow does not bypass independent host token scopes, protected targets or brakes.
No precedence rule, policy layering algorithm or permissive mode is defined here.

Every supplied obligation is mandatory. `Approval` carries an opaque host binding
reference, not a proof or bearer. The host retains no-self-approval, verified
presence, requester/argument/plan binding, expiry and one-time consumption.
`DryRunOnly` does not permit a real effect. `RateLimit` carries count/window/unit
and a host reference; the host owns keys, counters, reservations, refunds and
preview behavior. No evaluator-side approval fulfillment or rate math occurs.
An allow decision with obligations cannot execute until the host fulfills them.
Unknown kinds are invalid decisions; an explicit fail-open call records that
failure rather than silently dropping unsupported intent.

## One audit shape

The evaluator attempts one synchronous event before returning. Its decision,
policy provenance and obligation values are detached copies. There is no context,
raw tool input or underlying exception text in the event. `Error.Error()` exposes
only a fixed classification; `Unwrap` lets trusted host code inspect the cause.

`AuditEvent.ProposedAdmission` is conditional on the sink accepting the event.
If recording fails, the returned admission tightens to deny; no second event is
attempted and durable storage is not claimed. The host owns the audit sink and
any separate observation/read failure behavior, post-effect outcomes and queues.
This module never runs an operation. Its audit failure policy is not a migration
of an existing application's audit behavior.

## Recorded-case conformance scaffolding

`conformance.Check` takes caller-owned named requests and full expected decisions.
It detects changes in effect, reason, ordered policy provenance and obligations,
and refuses an empty or invalid corpus. Its tests project a small recorded subset
of the existing permission and layered policy cases into this vocabulary and
prove the checker detects deliberately wrong PDP results.

Those scripted PDPs are **checker tests**, not implementations or conformance
proof for the existing engines. No permission, layered YAML or Cedar adapter is
included. Actual host adapters must retain their original recorded case
provenance and prove their real behavior through this scaffold and host tests.
Policy schema validation, exception precedence and host compatibility projections
remain separate work. No consumers are wired to this new module.

## Dependencies and checks

Policy imports no Harness, agent lifecycle, mesh wire, application or Cedar types.
Harness and application adapters may depend on policy; the reverse is forbidden.
LLM core remains independent. No sibling module version is changed.

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

From the repository root, `scripts/check -race policy` runs the module gate.
The repository's existing CodeQL workflow covers mesh only, **not policy**.
No CodeQL coverage, engine integration, live enforcement or release is claimed.
