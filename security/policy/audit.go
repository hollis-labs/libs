package policy

import (
	"context"
	"time"
)

// AuditEvent is one pre-execution evaluation event. ProposedAdmission is
// conditional on a successful Record: audit failure tightens the returned
// admission to deny. No request context, raw tool input or underlying error text
// enters this shape. The host must project safe metadata into identity/resource,
// reason, policy provenance and obligation references before calling Evaluate.
type AuditEvent struct {
	At                time.Time
	RequestID         string
	Principal         Principal
	Action            string
	Resource          Resource
	Scope             string
	Mode              Mode
	FailMode          FailMode
	Decision          Decision
	ProposedAdmission Effect
	WouldBlock        bool
	Failure           FailureCode
}

// AuditSink synchronously accepts the single evaluation event. The host owns
// durability, access control and retention. Returning an error refuses admission;
// the evaluator does not retry, queue another event or execute anything.
type AuditSink interface {
	Record(context.Context, AuditEvent) error
}
