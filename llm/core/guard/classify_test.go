package guard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"
)

func TestClassifyStatus(t *testing.T) {
	cases := map[int]Class{
		200: ClassUnknown, 304: ClassUnknown,
		400: ClassRequest, 404: ClassRequest, 413: ClassRequest, 422: ClassRequest,
		401: ClassAuth, 403: ClassAuth,
		408: ClassTransient,
		429: ClassQuota,
		500: ClassTransient, 502: ClassTransient, 503: ClassTransient, 529: ClassTransient,
	}
	for status, want := range cases {
		if got := ClassifyStatus(status); got != want {
			t.Errorf("ClassifyStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	cases := []struct {
		name string
		h    http.Header
		want time.Duration
		ok   bool
	}{
		{"none", hdr(), 0, false},
		{"nil", nil, 0, false},
		{"seconds", hdr("Retry-After", "7"), 7 * time.Second, true},
		{"fractional seconds", hdr("Retry-After", "1.5"), 1500 * time.Millisecond, true},
		{"http date", hdr("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat)), 90 * time.Second, true},
		{"date in the past", hdr("Retry-After", now.Add(-time.Minute).Format(http.TimeFormat)), 0, true},
		{"milliseconds preferred", hdr("Retry-After-Ms", "250", "Retry-After", "9"), 250 * time.Millisecond, true},
		{"bad milliseconds falls back", hdr("Retry-After-Ms", "soon", "Retry-After", "2"), 2 * time.Second, true},
		{"negative", hdr("Retry-After", "-3"), 0, false},
		{"garbage", hdr("Retry-After", "later"), 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseRetryAfter(tc.h, now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestHTTPError(t *testing.T) {
	cause := errors.New("rate limited")
	e := HTTPError(429, http.Header{"Retry-After": []string{"3"}}, cause)
	if e.Class != ClassQuota || e.RetryAfter != 3*time.Second || e.StatusCode != 429 || !errors.Is(e, cause) {
		t.Fatalf("HTTPError = %+v", e)
	}
	class, retry := Classify(fmt.Errorf("call: %w", e))
	if class != ClassQuota || retry != 3*time.Second {
		t.Fatalf("Classify(wrapped HTTPError) = (%v, %v)", class, retry)
	}
	if got := e.Error(); got != "guard: quota (status 429): rate limited" {
		t.Fatalf("Error() = %q", got)
	}
}

type selfClassified struct{ c Class }

func (e selfClassified) Error() string     { return "self" }
func (e selfClassified) GuardClass() Class { return e.c }

type retryOnly struct{ d time.Duration }

func (e retryOnly) Error() string             { return "retry" }
func (e retryOnly) RetryAfter() time.Duration { return e.d }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		class Class
		retry time.Duration
	}{
		{"nil", nil, ClassUnknown, 0},
		{"plain", errors.New("boom"), ClassUnknown, 0},
		{"guard error", &Error{Class: ClassAuth}, ClassAuth, 0},
		{"classifier", fmt.Errorf("x: %w", selfClassified{ClassRequest}), ClassRequest, 0},
		{"retry-after only", retryOnly{2 * time.Second}, ClassUnknown, 2 * time.Second},
		{"retry-after on outer, class inside", &Error{Class: ClassQuota, Err: retryOnly{time.Second}}, ClassQuota, time.Second},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), ClassCanceled, 0},
		{"deadline", context.DeadlineExceeded, ClassTransient, 0},
		{"eof", io.ErrUnexpectedEOF, ClassConnection, 0},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, ClassConnection, 0},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), ClassConnection, 0},
		{"net timeout", &net.OpError{Op: "read", Err: timeoutErr{}}, ClassTransient, 0},
		{"refused call", &RefusedError{Decision: Decision{RetryAfter: time.Second}}, ClassCanceled, time.Second},
	}
	for _, tc := range cases {
		class, retry := Classify(tc.err)
		if class != tc.class || retry != tc.retry {
			t.Errorf("%s: Classify = (%v, %v), want (%v, %v)", tc.name, class, retry, tc.class, tc.retry)
		}
	}
}

func TestClassAndReasonStrings(t *testing.T) {
	for c, want := range map[Class]string{
		ClassUnknown: "unknown", ClassTransient: "transient", ClassConnection: "connection",
		ClassQuota: "quota", ClassAuth: "auth", ClassRequest: "request", ClassCanceled: "canceled",
	} {
		if c.String() != want {
			t.Errorf("Class %d = %q, want %q", c, c.String(), want)
		}
	}
	for r, want := range map[Reason]string{
		ReasonNone: "none", ReasonCooldown: "cooldown", ReasonQuota: "quota",
		ReasonCircuitOpen: "circuit-open", ReasonProbeInFlight: "probe-in-flight", 99: "unknown",
	} {
		if r.String() != want {
			t.Errorf("Reason %d = %q, want %q", r, r.String(), want)
		}
	}
}
