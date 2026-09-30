package tesseract

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	// ErrNotFound is matched by errors.Is for a 404 / not_found answer.
	ErrNotFound = errors.New("tesseract: not found")

	// ErrUnavailable is matched by errors.Is when Tesseract could not be
	// reached or answered something unusable: a transport failure (including
	// a cancelled or expired context, which also matches the context error),
	// a response body that could not be read or exceeded the size cap, or a
	// 2xx body that did not decode. A non-2xx status is an *APIError and does
	// not match, whatever the status. A caller can then say "Tesseract is
	// down" rather than "something failed".
	ErrUnavailable = errors.New("tesseract: unavailable")

	// ErrResponseTooLarge is matched by errors.Is when a response body
	// exceeded the client's cap (see WithMaxResponseBytes). Such an error also
	// matches ErrUnavailable. The body is never decoded from a truncated read.
	ErrResponseTooLarge = errors.New("tesseract: response exceeds size cap")
)

// APIError is Tesseract's own {code, message} refusal, carried through so its
// validation messages (which name the offending field) reach the caller.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Code is Tesseract's error code, e.g. "not_found" or "validation_error".
	// Empty when the body was not a Tesseract error object.
	Code string
	// Message is Tesseract's message, or the (truncated) raw body, or the
	// status text when the body was empty.
	Message string
	// Path is the request path, without query.
	Path string
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("tesseract refused %s (%d): %s: %s", e.Path, e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("tesseract refused %s (%d): %s", e.Path, e.Status, e.Message)
}

// Is makes a not-found answer match ErrNotFound.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && (e.Status == http.StatusNotFound || e.Code == "not_found")
}
