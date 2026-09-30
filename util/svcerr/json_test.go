package svcerr

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func write(err error, fallback int) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	WriteJSON(rec, err, fallback)
	return rec
}

func TestWriteJSONGolden(t *testing.T) {
	rec := write(fmt.Errorf("wrap: %w", New(CodeInvalid, "must not be empty", WithField("title"))), 500)
	if rec.Code != 400 {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	const want = `{"error":{"code":"invalid","message":"must not be empty","field":"title"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q\nwant   %q", got, want)
	}
}

func TestWriteJSONOmitsAnUnsetField(t *testing.T) {
	rec := write(New(CodeNotFound, "no such task"), 500)
	const want = `{"error":{"code":"not_found","message":"no such task"}}` + "\n"
	if got := rec.Body.String(); got != want || rec.Code != 404 {
		t.Errorf("status %d body %q", rec.Code, got)
	}
}

// A plain error must never have its own text written into the response: it may
// carry a query, a path or a credential.
func TestWriteJSONNeverLeaksAPlainErrorsText(t *testing.T) {
	secret := "pq: password authentication failed for user admin at 10.0.0.5"
	for name, err := range map[string]error{
		"plain":           errors.New(secret),
		"wrapped plain":   fmt.Errorf("repo: %w", errors.New(secret)),
		"cause of *Error": Wrap(errors.New(secret), CodeInternal, "internal error"),
		"nil":             nil,
	} {
		rec := write(err, http.StatusBadGateway)
		if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Errorf("%s: body leaked internal text: %q", name, rec.Body.String())
		}
	}
	rec := write(errors.New(secret), http.StatusBadGateway)
	const want = `{"error":{"code":"internal","message":"internal error"}}` + "\n"
	if rec.Code != http.StatusBadGateway || rec.Body.String() != want {
		t.Errorf("status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestWriteJSONNeverWritesAnInvalidStatus(t *testing.T) {
	for name, e := range map[string]error{
		"unknown code": New("teapot", "m"),
		"zero status":  New(CodeNotFound, "m", WithStatus(0)),
		"huge status":  New(CodeNotFound, "m", WithStatus(12345)),
		"plain":        errors.New("x"),
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: WriteJSON panicked: %v", name, r)
				}
			}()
			for _, fb := range []int{0, 99, 1000, -1, 500} {
				if rec := write(e, fb); !validStatus(rec.Code) {
					t.Errorf("%s fallback %d: wrote status %d", name, fb, rec.Code)
				}
			}
		}()
	}
}

func TestWriteJSONFillsAnEmptyMessageFromTheStatus(t *testing.T) {
	rec := write(&Error{Code: CodeNotFound}, 500)
	const want = `{"error":{"code":"not_found","message":"Not Found"}}` + "\n"
	if rec.Body.String() != want {
		t.Errorf("body = %q", rec.Body.String())
	}
}
