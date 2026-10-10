package guard

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Class says what an error means for the resource that returned it, which
// decides what the guard does with it.
type Class uint8

const (
	// ClassUnknown is an error nothing classified. The guard treats it as
	// upstream ill-health, as a breaker without classification would.
	ClassUnknown Class = iota
	// ClassTransient is upstream ill-health that may pass: a 5xx, an
	// overloaded response, a timeout. It counts against the breaker and
	// cools the key down.
	ClassTransient
	// ClassConnection is a failure of this connection rather than of the
	// account or the model: a refused dial, a reset, an unexpected EOF. It
	// counts against the breaker and sets no cooldown.
	ClassConnection
	// ClassQuota means the account has no budget left: a 429, an exhausted
	// quota. The upstream is healthy, so it does not count against the
	// breaker; it cools the key down until the provider's retry-after.
	ClassQuota
	// ClassAuth means the credentials were refused: a 401 or 403. It cools
	// the whole account down, for every model, and does not count against
	// the breaker.
	ClassAuth
	// ClassRequest means this request was wrong: malformed, too large, for a
	// model that does not exist. The upstream answered and the account is
	// fine, so it sets no cooldown and records as a breaker success.
	ClassRequest
	// ClassCanceled means the caller gave up. It says nothing about the
	// upstream and frees a probe slot without a verdict.
	ClassCanceled
)

// String returns the class name in lower case.
func (c Class) String() string {
	switch c {
	case ClassTransient:
		return "transient"
	case ClassConnection:
		return "connection"
	case ClassQuota:
		return "quota"
	case ClassAuth:
		return "auth"
	case ClassRequest:
		return "request"
	case ClassCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// Error is an error with its Class and the provider's retry-after attached.
// Provider adapters return it (or wrap it) so the guard does not have to
// guess.
type Error struct {
	Class Class
	// RetryAfter is the wait the provider asked for; zero when it named none.
	RetryAfter time.Duration
	// StatusCode is the HTTP status, when there was one.
	StatusCode int
	// Err is the underlying error. It may be nil.
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("guard: ")
	b.WriteString(e.Class.String())
	if e.StatusCode != 0 {
		b.WriteString(" (status ")
		b.WriteString(strconv.Itoa(e.StatusCode))
		b.WriteString(")")
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error { return e.Err }

// Classifier is implemented by error types that know their own Class. An
// adapter's error type can implement it instead of being wrapped in Error.
type Classifier interface {
	GuardClass() Class
}

// RetryAfterer is implemented by error types that carry a provider
// retry-after.
type RetryAfterer interface {
	RetryAfter() time.Duration
}

// Classify returns err's Class and the retry-after it carries. It looks, in
// order, for an *Error, a Classifier and a RetryAfterer in err's chain, then
// recognises context, timeout and connection errors from the standard
// library. Anything else is ClassUnknown. A nil err is ClassUnknown with no
// retry-after.
func Classify(err error) (Class, time.Duration) {
	if err == nil {
		return ClassUnknown, 0
	}
	var retry time.Duration
	var ra RetryAfterer
	if errors.As(err, &ra) {
		retry = max(ra.RetryAfter(), 0)
	}
	var ge *Error
	if errors.As(err, &ge) {
		if ge.RetryAfter > 0 {
			retry = ge.RetryAfter
		}
		return ge.Class, retry
	}
	var c Classifier
	if errors.As(err, &c) {
		return c.GuardClass(), retry
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ClassCanceled, retry
	case errors.Is(err, context.DeadlineExceeded):
		return ClassTransient, retry
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.EPIPE), errors.Is(err, net.ErrClosed):
		return ClassConnection, retry
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return ClassTransient, retry
		}
		return ClassConnection, retry
	}
	return ClassUnknown, retry
}

// ClassifyStatus maps an HTTP status code to a Class. 2xx and 3xx are
// ClassUnknown: they are not errors.
func ClassifyStatus(status int) Class {
	switch {
	case status == http.StatusTooManyRequests:
		return ClassQuota
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ClassAuth
	case status == http.StatusRequestTimeout:
		return ClassTransient
	case status >= 400 && status < 500:
		return ClassRequest
	case status >= 500:
		return ClassTransient
	default:
		return ClassUnknown
	}
}

// ParseRetryAfter reads the retry-after a response carries. It prefers the
// millisecond header some LLM providers send (retry-after-ms), then the
// standard Retry-After in seconds or as an HTTP date relative to now. It
// returns false when neither is present or parseable; a date in the past is
// zero.
func ParseRetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if h == nil {
		return 0, false
	}
	if v := strings.TrimSpace(h.Get("Retry-After-Ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs * float64(time.Second)), true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

// HTTPError classifies a failed HTTP response: its Class from the status and
// its RetryAfter from the headers, wrapping err. Adapters that also read the
// response body (for example an "insufficient_quota" or "overloaded" error
// type) can adjust Class before returning it.
func HTTPError(status int, h http.Header, err error) *Error {
	ra, _ := ParseRetryAfter(h, SystemClock.Now())
	return &Error{Class: ClassifyStatus(status), RetryAfter: ra, StatusCode: status, Err: err}
}
