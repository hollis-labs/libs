package svcerr

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a machine-readable error category. It is an open string type: the six
// constants below cover the categories the surveyed apps share, and an app may
// define its own (give it a status with [WithStatus], since only the built-in
// codes have a default).
type Code string

// The built-in codes.
const (
	CodeInvalid     Code = "invalid"
	CodeNotFound    Code = "not_found"
	CodeConflict    Code = "conflict"
	CodePermission  Code = "permission"
	CodeUnavailable Code = "unavailable"
	CodeInternal    Code = "internal"
)

// defaultStatus is the HTTP status each built-in code maps to unless a caller
// overrides it with [WithStatus].
var defaultStatus = map[Code]int{
	CodeInvalid:     http.StatusBadRequest,
	CodeNotFound:    http.StatusNotFound,
	CodeConflict:    http.StatusConflict,
	CodePermission:  http.StatusForbidden,
	CodeUnavailable: http.StatusServiceUnavailable,
	CodeInternal:    http.StatusInternalServerError,
}

// DefaultStatus returns the HTTP status a built-in code maps to, and false for
// a code the package does not know.
func DefaultStatus(c Code) (int, bool) {
	s, ok := defaultStatus[c]
	return s, ok
}

// Error is the shared carrier. Message is the safe, external-facing text; Err,
// when set, is the full internal error for the caller to log. Err is never
// rendered into Message or Error() automatically.
type Error struct {
	// Status is the HTTP status. New and Wrap fill it from the code; a zero or
	// non-error (outside 400..599) Status (for example on a struct literal) is treated as
	// "unset" by [StatusFor] and [WriteJSON], which then use the code's default.
	Status  int
	Code    Code
	Message string
	// Field names the offending input field of an invalid-input error.
	Field string
	Err   error
}

// Error renders the code and the safe message (and the field, when set). It
// never includes the wrapped error's text.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Field != "" {
		return fmt.Sprintf("%s: %s (field=%s)", e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped internal error, if any, so errors.Is and
// errors.As keep working through an Error.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is reports whether target is an *Error with the same Code, so
// errors.Is(err, svcerr.ErrNotFound) matches any not-found Error whatever its
// message or status.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e != nil && t != nil && t.Code == e.Code
}

// The category sentinels, for errors.Is. They carry their code's default
// status, so returning one directly is safe. Do not modify them.
var (
	ErrInvalid     = sentinel(CodeInvalid)
	ErrNotFound    = sentinel(CodeNotFound)
	ErrConflict    = sentinel(CodeConflict)
	ErrPermission  = sentinel(CodePermission)
	ErrUnavailable = sentinel(CodeUnavailable)
	ErrInternal    = sentinel(CodeInternal)
)

func sentinel(c Code) *Error {
	return &Error{Status: defaultStatus[c], Code: c, Message: string(c)}
}

// Option adjusts an Error under construction.
type Option func(*Error)

// WithField names the offending input field.
func WithField(field string) Option { return func(e *Error) { e.Field = field } }

// WithStatus overrides the code's default HTTP status, for a case the default
// mapping does not fit (or for a custom code, which has no default).
func WithStatus(status int) Option { return func(e *Error) { e.Status = status } }

// Wrap returns an Error carrying err as its internal cause. message is the safe
// text; it is never taken from err.
func Wrap(err error, code Code, message string, opts ...Option) *Error {
	e := &Error{Status: defaultStatus[code], Code: code, Message: message, Err: err}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// New returns an Error with no internal cause.
func New(code Code, message string, opts ...Option) *Error { return Wrap(nil, code, message, opts...) }

// status returns e's usable HTTP status: its own when valid, else its code's
// default, else 500.
func (e *Error) status() int {
	if validStatus(e.Status) {
		return e.Status
	}
	if s, ok := defaultStatus[e.Code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// validStatus reports whether s is a usable ERROR status: 400..599. Anything
// else is refused, not just what http.ResponseWriter.WriteHeader would panic on
// (outside 100..999): an error body under a 1xx, 2xx or 3xx status is a failure
// reported as success or a redirect, and a 204 cannot carry a body at all, so
// the client would see an empty success for a failure.
func validStatus(s int) bool { return s >= 400 && s <= 599 }

// StatusFor returns the HTTP status of the first *Error in err's chain, or
// fallback when there is none. It never inspects the error's text.
func StatusFor(err error, fallback int) int {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e.status()
	}
	return fallback
}

// CodeFor returns the Code of the first *Error in err's chain, or "" when there
// is none.
func CodeFor(err error) Code {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e.Code
	}
	return ""
}
