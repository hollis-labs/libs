package transportparity

import (
	"fmt"

	svcerr "github.com/hollis-labs/libs/util/svcerr"
)

// T is the part of *testing.T (or testing.TB) the assertions use. It is a small
// interface so the package can test its own failure paths.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Outcome is what a caller reduces ONE transport's result to.
type Outcome struct {
	// OK is true for a success.
	OK bool
	// Value is the decoded success value. It is compared with [CanonicalJSON].
	Value any
	// Category is the failure category. It must be set on a failure.
	Category svcerr.Code
	// Status is the transport's own status, kept for the failure message. It is
	// never compared: an HTTP status and a JSON-RPC code will not be equal.
	Status int
	// Detail is the transport's own error text, kept for the failure message. It
	// is never compared: wording differs between doors by design.
	Detail string
}

// FromValue builds a success Outcome.
func FromValue(v any) Outcome { return Outcome{OK: true, Value: v} }

// FromError builds a failure Outcome. When err is or wraps a *svcerr.Error its
// code and status are used; otherwise fallbackCategory and fallbackStatus are.
// Pass a real fallbackCategory: a failure with no category cannot be compared,
// and [AssertSameOutcome] rejects one. A nil err is a caller mistake and yields
// a failure with Detail "<nil error>".
func FromError(err error, fallbackCategory svcerr.Code, fallbackStatus int) Outcome {
	if err == nil {
		return Outcome{Category: fallbackCategory, Status: fallbackStatus, Detail: "<nil error>"}
	}
	cat := svcerr.CodeFor(err)
	if cat == "" {
		cat = fallbackCategory
	}
	return Outcome{
		Category: cat,
		Status:   svcerr.StatusFor(err, fallbackStatus),
		Detail:   err.Error(),
	}
}

func (o Outcome) String() string {
	if o.OK {
		return "ok"
	}
	return fmt.Sprintf("failed category=%q status=%d detail=%q", o.Category, o.Status, o.Detail)
}

// AssertSameOutcome fails t unless two transports' Outcomes for the same
// logical input agree: both OK or both not; on failure, the same non-empty
// Category; on success, CanonicalJSON-equal Values. caseName labels the failure.
// Status and Detail are never compared.
func AssertSameOutcome(t T, caseName string, a, b Outcome) {
	t.Helper()
	switch {
	case a.OK != b.OK:
		t.Errorf("%s: transports disagree on success: A %s, B %s", caseName, a, b)
	case !a.OK:
		if a.Category == "" || b.Category == "" {
			t.Errorf("%s: both failed but a failure has no category (A %q, B %q); pass a fallbackCategory to FromError, an uncategorized failure cannot be compared", caseName, a.Category, b.Category)
		} else if a.Category != b.Category {
			t.Errorf("%s: transports disagree on the failure category: A %s, B %s", caseName, a, b)
		}
	default:
		ca, cb := CanonicalJSON(t, a.Value), CanonicalJSON(t, b.Value)
		if ca != cb {
			t.Errorf("%s: transports succeed with different values:\n  A: %s\n  B: %s", caseName, ca, cb)
		}
	}
}
