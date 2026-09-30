package svcerr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestIsMatchesThroughWrappingLayers(t *testing.T) {
	base := New(CodeNotFound, "no such task")
	wrapped := fmt.Errorf("service: %w", fmt.Errorf("repo: %w", base))
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("errors.Is did not match ErrNotFound through two wrapping layers")
	}
	for _, other := range []error{ErrInvalid, ErrConflict, ErrPermission, ErrUnavailable, ErrInternal} {
		if errors.Is(wrapped, other) {
			t.Errorf("a not_found error matched %v", other)
		}
	}
	if errors.Is(errors.New("plain"), ErrNotFound) {
		t.Error("a plain error matched a sentinel")
	}
}

func TestAsUnwrapsThroughAGenericWrapper(t *testing.T) {
	err := fmt.Errorf("handler: %w", Wrap(errors.New("sql: no rows"), CodeNotFound, "no such task", WithField("id")))
	var got *Error
	if !errors.As(err, &got) {
		t.Fatal("errors.As did not find the *Error")
	}
	if got.Code != CodeNotFound || got.Field != "id" || got.Message != "no such task" {
		t.Errorf("got %+v", got)
	}
}

// Unwrap keeps the internal cause reachable, so a caller can log it or match a
// driver error, without the cause ever being rendered.
func TestTheInternalCauseIsReachableButNeverRendered(t *testing.T) {
	cause := errors.New("pq: password authentication failed for user admin")
	err := Wrap(cause, CodeInternal, "internal error")
	if !errors.Is(err, cause) {
		t.Error("the wrapped cause is not reachable with errors.Is")
	}
	if strings.Contains(err.Error(), "password") {
		t.Errorf("Error() leaked the cause: %q", err.Error())
	}
}

// A plain error has no status here. StatusFor hands back the caller's fallback
// instead of guessing from the error text, the gap two apps papered over by
// substring-matching English wording.
func TestStatusForAndCodeForDoNotGuessFromText(t *testing.T) {
	for _, msg := range []string{"task not found", "field is required", "unsupported format", "no rows in result set"} {
		err := errors.New(msg)
		if got := StatusFor(err, http.StatusTeapot); got != http.StatusTeapot {
			t.Errorf("StatusFor(%q) = %d, want the fallback", msg, got)
		}
		if got := CodeFor(err); got != "" {
			t.Errorf("CodeFor(%q) = %q, want empty", msg, got)
		}
	}
	if got := StatusFor(nil, 418); got != 418 {
		t.Errorf("StatusFor(nil) = %d", got)
	}
}

func TestStatusForAndCodeForReadTheChain(t *testing.T) {
	err := fmt.Errorf("wrap: %w", New(CodeConflict, "version clash"))
	if got := StatusFor(err, 500); got != http.StatusConflict {
		t.Errorf("StatusFor = %d", got)
	}
	if got := CodeFor(err); got != CodeConflict {
		t.Errorf("CodeFor = %q", got)
	}
}

func TestDefaultStatusMapping(t *testing.T) {
	want := map[Code]int{
		CodeInvalid: 400, CodeNotFound: 404, CodeConflict: 409,
		CodePermission: 403, CodeUnavailable: 503, CodeInternal: 500,
	}
	for c, s := range want {
		if got, ok := DefaultStatus(c); !ok || got != s {
			t.Errorf("DefaultStatus(%s) = %d, %v; want %d", c, got, ok, s)
		}
		if got := StatusFor(New(c, "m"), 0); got != s {
			t.Errorf("StatusFor(New(%s)) = %d, want %d", c, got, s)
		}
	}
	if _, ok := DefaultStatus("approval_required"); ok {
		t.Error("an app-defined code should have no default status")
	}
}

func TestWithStatusOverridesTheDefault(t *testing.T) {
	err := New(CodePermission, "approval needed", WithStatus(http.StatusPaymentRequired))
	if got := StatusFor(err, 0); got != http.StatusPaymentRequired {
		t.Errorf("StatusFor = %d", got)
	}
}

// Returning a sentinel directly must carry a usable status, and a struct
// literal or unknown code with no status must never yield 0: WriteHeader panics
// outside 100..999 and a zero status is an unset one.
func TestStatusIsNeverZeroOrInvalid(t *testing.T) {
	for _, s := range []*Error{ErrInvalid, ErrNotFound, ErrConflict, ErrPermission, ErrUnavailable, ErrInternal} {
		if !validStatus(s.Status) {
			t.Errorf("sentinel %s has status %d", s.Code, s.Status)
		}
	}
	cases := map[string]*Error{
		"literal with no status":   {Code: CodeNotFound, Message: "m"},
		"custom code, no status":   New("teapot", "m"),
		"explicit zero status":     New(CodeNotFound, "m", WithStatus(0)),
		"status below 100":         New(CodeNotFound, "m", WithStatus(42)),
		"status above 999":         New(CodeNotFound, "m", WithStatus(1000)),
		"empty code, empty status": {},
	}
	for name, e := range cases {
		got := StatusFor(e, 0)
		if !validStatus(got) {
			t.Errorf("%s: StatusFor = %d", name, got)
		}
	}
	if got := StatusFor(cases["literal with no status"], 0); got != http.StatusNotFound {
		t.Errorf("an unset status should fall back to the code's default: got %d", got)
	}
	if got := StatusFor(cases["custom code, no status"], 0); got != http.StatusInternalServerError {
		t.Errorf("an unknown code with no status should be 500: got %d", got)
	}
}

func TestErrorStringWithAndWithoutField(t *testing.T) {
	if got := New(CodeInvalid, "must not be empty").Error(); got != "invalid: must not be empty" {
		t.Errorf("got %q", got)
	}
	if got := New(CodeInvalid, "must not be empty", WithField("title")).Error(); got != "invalid: must not be empty (field=title)" {
		t.Errorf("got %q", got)
	}
}

func TestNilReceiversDoNotPanic(t *testing.T) {
	var e *Error
	if e.Error() == "" || e.Unwrap() != nil || e.Is(ErrNotFound) {
		t.Error("nil *Error methods misbehave")
	}
	if StatusFor((*Error)(nil), 7) != 7 || CodeFor((*Error)(nil)) != "" {
		t.Error("a typed-nil *Error should read as no error")
	}
}

var _ error = (*Error)(nil)
