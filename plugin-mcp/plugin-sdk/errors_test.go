package plugin

import (
	"errors"
	"testing"
)

func TestErrorCodes(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		wantMsg  string
		wantCode int
	}{
		{"not found", ErrNotFound("missing widget"), "missing widget", 404},
		{"conflict", ErrConflict("duplicate id"), "duplicate id", 409},
		{"validation", ErrValidation("bad input"), "bad input", 422},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Code != tc.wantCode {
				t.Errorf("Code = %d, want %d", tc.err.Code, tc.wantCode)
			}
			if tc.err.Error() != tc.wantMsg {
				t.Errorf("Error() = %q, want %q", tc.err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestErrorImplementsErrorInterface(t *testing.T) {
	var err error = ErrNotFound("x")
	if err == nil {
		t.Fatal("ErrNotFound returned nil")
	}
	var target *Error
	if !errors.As(err, &target) {
		t.Fatalf("errors.As failed for *Error")
	}
	if target.Code != 404 {
		t.Errorf("Code = %d, want 404", target.Code)
	}
}

func TestPluginErrorAlias(t *testing.T) {
	// PluginError must be usable as an alias for Error.
	var e *PluginError = ErrConflict("x")
	if e.Code != 409 {
		t.Errorf("alias carries wrong code: %d", e.Code)
	}
}

func TestErrCancelledIsSentinel(t *testing.T) {
	if ErrCancelled == nil {
		t.Fatal("ErrCancelled must not be nil")
	}
	wrapped := errors.New("wrapper: " + ErrCancelled.Error())
	if errors.Is(wrapped, ErrCancelled) {
		t.Errorf("plain string wrap should not match sentinel")
	}
	if !errors.Is(ErrCancelled, ErrCancelled) {
		t.Errorf("sentinel must match itself via errors.Is")
	}
}
