package plugin

import "errors"

// ErrCancelled is returned by an EventHook's Handle method to signal that
// the pre-hook action should be cancelled. Only meaningful for pre-hook
// events (e.g., message.sending, tool.executing). Regular post-events
// ignore this value.
var ErrCancelled = errors.New("plugin: action cancelled by hook")

// Error is a typed plugin error that carries an HTTP-friendly status code
// plus a stable code constant for wire-protocol mapping. Plugin CRUD
// handlers should return these instead of plain errors so the host can
// translate them into HTTP status codes and JSON-RPC error objects.
//
// PluginError is a backward-compatibility alias retained for callers
// that imported the type under its older name; new code should use
// Error directly.
type Error struct {
	Code    int // HTTP status code (404, 409, 422, etc.)
	Message string
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

// PluginError is a backward-compatible alias for Error.
//
// Deprecated: use Error directly. This alias will be removed in a future
// release.
type PluginError = Error

// Sentinel constructors for common CRUD errors.

// ErrNotFound returns a 404 Error.
func ErrNotFound(msg string) *Error { return &Error{Code: 404, Message: msg} }

// ErrConflict returns a 409 Error.
func ErrConflict(msg string) *Error { return &Error{Code: 409, Message: msg} }

// ErrValidation returns a 422 Error.
func ErrValidation(msg string) *Error { return &Error{Code: 422, Message: msg} }
