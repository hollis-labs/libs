package tether

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
)

var ErrDaemonUnreachable = errors.New("tether daemon unreachable")

// ErrSelfURNRequired is returned by httpStore.Get and httpStore.Thread when
// the Client was constructed without WithSelfURN. Both calls have no
// recipient/address parameter to derive Tether's required `?as=` claim
// from, so failing fast client-side (rather than letting the daemon return
// a 400) gives a caller a clear, actionable error instead of an opaque API
// failure.
var ErrSelfURNRequired = errors.New("tether: WithSelfURN must be configured to call Get or Thread")

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("tether %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	if e.Code != "" {
		return fmt.Sprintf("tether %d (%s)", e.StatusCode, e.Code)
	}
	if e.Message != "" {
		return fmt.Sprintf("tether %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("tether %d: %s", e.StatusCode, e.Body)
}

func (e *APIError) Is(target error) bool {
	t, ok := target.(*APIError)
	if !ok {
		return false
	}
	if t.StatusCode != 0 && e.StatusCode != t.StatusCode {
		return false
	}
	if t.Code != "" && e.Code != t.Code {
		return false
	}
	return true
}

func readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var env ErrorResponse
	if err := json.Unmarshal(body, &env); err == nil && (env.Error.Code != "" || env.Error.Message != "") {
		return &APIError{
			StatusCode: resp.StatusCode,
			Code:       env.Error.Code,
			Message:    env.Error.Message,
			Body:       string(body),
		}
	}
	return &APIError{StatusCode: resp.StatusCode, Body: string(body)}
}

func wrapIfUnreachable(err error) error {
	if isUnreachable(err) {
		return fmt.Errorf("%w: %w", ErrDaemonUnreachable, err)
	}
	return err
}

func isUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") || strings.Contains(s, "no such file or directory")
}
