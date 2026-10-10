package policy

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Principal is identity already verified by the host. Constructing this value
// does not authenticate a caller or grant authority. OnBehalfOf is an optional
// verified delegation identity, not a caller-supplied label.
type Principal struct {
	URN        string
	Kind       string
	OnBehalfOf string
}

// Resource names the host-resolved object to which a policy applies.
type Resource struct {
	Type string
	ID   string
}

// Request carries host-projected facts. Context may contain sensitive input;
// the evaluator never copies it into an audit event. RequestID is optional host
// correlation metadata. Scope identifies the host's enforcement scope.
type Request struct {
	RequestID string
	Principal Principal
	Action    string
	Resource  Resource
	Scope     string
	Context   map[string]any
}

// Validate checks contract shape, not identity authenticity or policy schema.
func (r Request) Validate() error {
	if !identityReference(r.Principal.URN) || strings.TrimSpace(r.Principal.Kind) == "" ||
		strings.TrimSpace(r.Action) == "" || strings.TrimSpace(r.Scope) == "" ||
		strings.TrimSpace(r.Resource.Type) == "" || strings.TrimSpace(r.Resource.ID) == "" {
		return errors.New("policy: invalid request metadata")
	}
	if r.Principal.OnBehalfOf != "" && !identityReference(r.Principal.OnBehalfOf) {
		return errors.New("policy: invalid delegation reference")
	}
	return nil
}

func identityReference(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme != "" && (u.Host != "" || u.Opaque != "")
}

// Effect is the evaluated decision vocabulary. Ask remains a refusal to execute
// until the host's approval process succeeds; it is not an approval receipt.
type Effect string

const (
	Allow Effect = "allow"
	Ask   Effect = "ask"
	Deny  Effect = "deny"
)

// PolicyMatch retains ordered policy provenance and a safe, host-authored reason.
// These fields are audit metadata: no raw arguments, credentials or payloads.
type PolicyMatch struct {
	ID     string
	Source string
	Reason string
}

type ObligationKind string

const (
	Approval   ObligationKind = "approval"
	DryRunOnly ObligationKind = "dry_run_only"
	RateLimit  ObligationKind = "rate_limit"
)

// Limit is policy intent in host-defined units, never a counter or reservation.
// For example, two operations per hour permits two before the host refuses the
// third. The host owns caller/effect keys, previews, refunds and durable accounting.
type Limit struct {
	Count  int64
	Window time.Duration
	Unit   string
}

// Obligation is mandatory policy intent. Reference is an opaque host binding or
// rule reference, never a bearer or proof of fulfillment. Approval brokers retain
// no-self-approval, presence, requester and plan/argument binding checks. Rate
// limits retain host counters. DryRunOnly never authorizes a real effect.
// Unknown kinds and incompatible payloads are invalid, not optional diagnostics.
type Obligation struct {
	Kind      ObligationKind
	PolicyID  string
	Reference string
	Limit     *Limit
}

func (o Obligation) Validate() error {
	if strings.TrimSpace(o.PolicyID) == "" {
		return errors.New("policy: obligation policy reference required")
	}
	switch o.Kind {
	case Approval:
		if o.Reference == "" || o.Limit != nil {
			return errors.New("policy: invalid approval obligation")
		}
	case DryRunOnly:
		if o.Limit != nil {
			return errors.New("policy: invalid dry-run obligation")
		}
	case RateLimit:
		if o.Reference == "" || o.Limit == nil || o.Limit.Count <= 0 || o.Limit.Window <= 0 || strings.TrimSpace(o.Limit.Unit) == "" {
			return errors.New("policy: invalid rate obligation")
		}
	default:
		return errors.New("policy: unsupported obligation")
	}
	return nil
}

// Decision preserves evaluator intent. It neither combines policies nor fulfills
// obligations. Reason and matched policy fields must be safe audit metadata.
type Decision struct {
	Effect          Effect
	Reason          string
	MatchedPolicies []PolicyMatch
	Obligations     []Obligation
}

func (d Decision) Validate() error {
	if d.Effect != Allow && d.Effect != Ask && d.Effect != Deny {
		return errors.New("policy: invalid decision effect")
	}
	for _, match := range d.MatchedPolicies {
		if strings.TrimSpace(match.ID) == "" {
			return errors.New("policy: matched policy id required")
		}
	}
	for _, obligation := range d.Obligations {
		if err := obligation.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// PDP is the swappable policy decision point. An error means there is no usable
// decision; a partial decision returned with an error cannot authorize execution.
type PDP interface {
	Evaluate(context.Context, Request) (Decision, error)
}

type Mode string

const (
	Enforce Mode = "enforce"
	Shadow  Mode = "shadow"
)

type FailMode string

const (
	FailClosed        FailMode = "closed"
	FailOpenWithAudit FailMode = "open_with_audit"
)

// Options is explicit per call. Zero/unknown values refuse, rather than inheriting
// a process-wide default. The host chooses these under its enforcement policy.
type Options struct {
	Mode     Mode
	FailMode FailMode
}

func (o Options) Validate() error {
	if o.Mode != Enforce && o.Mode != Shadow || o.FailMode != FailClosed && o.FailMode != FailOpenWithAudit {
		return errors.New("policy: invalid evaluation options")
	}
	return nil
}
