package secrets

import (
	"errors"
	"runtime"
)

// Kind classifies why a secret could not be resolved. The values are stable
// and safe to put on a wire.
type Kind string

const (
	// KindNotFound: the backend is working and has no such entry.
	KindNotFound Kind = "not_found"
	// KindDenied: the backend refused: a locked or canceled keychain prompt,
	// a file or entry the caller may not read.
	KindDenied Kind = "denied"
	// KindUnavailable: the backend could not be reached: no Secret Service on a
	// headless host, a helper that is missing, a timeout.
	KindUnavailable Kind = "unavailable"
	// KindInvalid: the reference or the thing it points at is malformed or
	// unsafe, for example a credential file other users can read.
	KindInvalid Kind = "invalid"
	// KindEmpty: the entry exists and is empty.
	KindEmpty Kind = "empty"
	// KindResolvedToRef: the backend returned another reference instead of a
	// value. Typically a helper that echoed its input.
	KindResolvedToRef Kind = "resolved_to_ref"
	// KindTooLarge: the value is larger than the backend or this package allows.
	KindTooLarge Kind = "too_large"
	// KindUnsupportedScheme: no backend is registered for the scheme.
	KindUnsupportedScheme Kind = "unsupported_scheme"
)

// Error is the one error type this package returns.
//
// Error never renders Cause, and nothing in it is a secret value: Ref is the
// reference (or a redacted stub of it), Source names where the lookup went,
// and Reason is text this package wrote. Cause stays reachable through Unwrap,
// so errors.Is and errors.As still see an os error or a context deadline.
type Error struct {
	Kind   Kind
	Ref    string
	Source string
	Reason string
	Cause  error
}

// Sentinels for errors.Is. They match any *Error of the same Kind.
var (
	ErrNotFound          = &Error{Kind: KindNotFound}
	ErrDenied            = &Error{Kind: KindDenied}
	ErrUnavailable       = &Error{Kind: KindUnavailable}
	ErrInvalid           = &Error{Kind: KindInvalid}
	ErrEmpty             = &Error{Kind: KindEmpty}
	ErrResolvedToRef     = &Error{Kind: KindResolvedToRef}
	ErrTooLarge          = &Error{Kind: KindTooLarge}
	ErrUnsupportedScheme = &Error{Kind: KindUnsupportedScheme}
)

// Error implements error.
func (e *Error) Error() string {
	s := "secrets: " + string(e.Kind)
	if e.Ref != "" {
		s += ": " + e.Ref
	}
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	return s
}

// Unwrap returns the underlying cause, which Error never renders.
func (e *Error) Unwrap() error { return e.Cause }

// Is matches a sentinel of the same Kind, or an *Error of the same Kind and Ref.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return t.Kind == e.Kind && (t.Ref == "" || t.Ref == e.Ref)
}

// Code is a stable machine-readable code such as "secret_not_found".
func (e *Error) Code() string { return "secret_" + string(e.Kind) }

// Guidance says what an operator can do about the failure. It does not depend
// on the value or the backend's error text. The per-OS wording is unverified
// against each platform's current UI.
func (e *Error) Guidance() string {
	switch e.Kind {
	case KindNotFound:
		return "No entry exists for this reference. Create it first, then retry."
	case KindDenied:
		return "The credential store refused access. Unlock it or approve the access prompt, then retry."
	case KindUnavailable:
		switch runtime.GOOS {
		case "darwin":
			return "The credential backend is unavailable. Unlock the login keychain, or check that the helper is installed and on PATH."
		case "linux":
			return "The credential backend is unavailable. A Secret Service provider (for example gnome-keyring or KeePassXC) must be running and unlocked. On a headless host use a file:// reference or an explicit env:// reference instead."
		case "windows":
			return "The credential backend is unavailable. Check that Windows Credential Manager is reachable for this user."
		}
		return "The credential backend is unavailable. Check that the store or helper is installed and reachable."
	case KindInvalid:
		return "The reference or the credential it points at is malformed or unsafe. Fix the reference or the file's permissions, then retry."
	case KindEmpty:
		return "The entry exists but holds no value. Store a value in it, then retry."
	case KindResolvedToRef:
		return "The backend returned another reference instead of a value. Check the helper and strip reference-valued variables from its environment."
	case KindTooLarge:
		return "The value is larger than the backend allows."
	case KindUnsupportedScheme:
		return "No backend is registered for this scheme."
	}
	return ""
}

// KindOf returns the Kind of err, or "" if err is not (or does not wrap) an *Error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

func newErr(kind Kind, ref Ref, source, reason string, cause error) *Error {
	return &Error{Kind: kind, Ref: ref.Raw, Source: source, Reason: reason, Cause: cause}
}
