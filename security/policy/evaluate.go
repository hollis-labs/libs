package policy

import (
	"context"
	"errors"
	"slices"
	"time"
)

type FailureCode string

const (
	InvalidCall      FailureCode = "invalid_call"
	InvalidRequest   FailureCode = "invalid_request"
	PDPUnavailable   FailureCode = "pdp_unavailable"
	PDPFailed        FailureCode = "pdp_failed"
	InvalidDecision  FailureCode = "invalid_decision"
	Canceled         FailureCode = "canceled"
	AuditUnavailable FailureCode = "audit_unavailable"
)

var ErrAuditUnavailable = errors.New("policy: audit unavailable")

// Error exposes a fixed classification without printing an underlying error
// that may contain private input. Unwrap supports host-side errors.Is/As checks.
type Error struct {
	Code  FailureCode
	cause error
}

func (e *Error) Error() string { return "policy: " + string(e.Code) }
func (e *Error) Unwrap() error { return e.cause }

// Result separates policy intent from this boundary's effective admission.
// Admission=allow does not fulfill Decision.Obligations or override independent
// host scopes, brakes or protected-target checks. WouldBlock describes an ask or
// deny policy decision, not the outcome of host-owned obligation checks.
type Result struct {
	Decision      Decision
	Admission     Effect
	WouldBlock    bool
	Failure       FailureCode
	AuditRecorded bool
}

// Evaluator is stateless. PDP and Audit must support concurrent calls when the
// host uses it concurrently. No engine, counter or grant state is stored here.
type Evaluator struct {
	PDP   PDP
	Audit AuditSink
}

// Evaluate calls the PDP at most once and attempts one audit event. Invalid call
// metadata and cancellation always refuse. PDP failure may fail open only with
// explicit FailOpenWithAudit and a successful audit write. The returned error is
// retained even for that fail-open result; hosts must explicitly inspect it.
func (e Evaluator) Evaluate(ctx context.Context, req Request, opts Options) (Result, error) {
	result := Result{Admission: Deny}
	var cause error
	fail := func(code FailureCode, err error, mayOpen bool) {
		result.Failure, cause = code, err
		result.Decision = Decision{Effect: Deny, Reason: string(code)}
		result.WouldBlock = true
		if mayOpen && opts.FailMode == FailOpenWithAudit {
			result.Admission = Allow
		}
	}
	if ctx == nil {
		ctx = context.Background()
		fail(InvalidCall, nil, false)
	} else if err := opts.Validate(); err != nil {
		fail(InvalidCall, err, false)
	} else if err := req.Validate(); err != nil {
		fail(InvalidRequest, err, false)
	} else if err := ctx.Err(); err != nil {
		fail(Canceled, err, false)
	} else if e.PDP == nil {
		fail(PDPUnavailable, nil, true)
	} else {
		decision, err := e.PDP.Evaluate(ctx, req)
		switch {
		case ctx.Err() != nil:
			fail(Canceled, ctx.Err(), false)
		case err != nil:
			fail(PDPFailed, err, true)
		default:
			if err := decision.Validate(); err != nil {
				fail(InvalidDecision, err, true)
			} else {
				result.Decision = cloneDecision(decision)
				result.Admission = decision.Effect
				result.WouldBlock = decision.Effect != Allow
				if opts.Mode == Shadow {
					result.Admission = Allow
				}
			}
		}
	}
	// req metadata is captured independently of the PDP result. The event contains
	// no Context and is detached from the returned result and PDP-owned slices.
	event := AuditEvent{At: time.Now().UTC(), RequestID: req.RequestID,
		Principal: req.Principal, Action: req.Action, Resource: req.Resource, Scope: req.Scope,
		Mode: opts.Mode, FailMode: opts.FailMode, Decision: cloneDecision(result.Decision),
		ProposedAdmission: result.Admission, WouldBlock: result.WouldBlock, Failure: result.Failure}
	var auditErr error
	if e.Audit == nil {
		auditErr = ErrAuditUnavailable
	} else {
		auditErr = e.Audit.Record(ctx, event)
	}
	if auditErr != nil {
		result.Admission, result.Failure = Deny, AuditUnavailable
		return result, &Error{Code: AuditUnavailable, cause: errors.Join(ErrAuditUnavailable, cause, auditErr)}
	}
	result.AuditRecorded = true
	if cause != nil || result.Failure != "" {
		return result, &Error{Code: result.Failure, cause: cause}
	}
	return result, nil
}

func cloneDecision(d Decision) Decision {
	d.MatchedPolicies = slices.Clone(d.MatchedPolicies)
	d.Obligations = slices.Clone(d.Obligations)
	for i := range d.Obligations {
		if d.Obligations[i].Limit != nil {
			limit := *d.Obligations[i].Limit
			d.Obligations[i].Limit = &limit
		}
	}
	return d
}
