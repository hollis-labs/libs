package chat

import (
	"errors"
	"fmt"
)

// Sentinel errors. Match them with errors.Is.
var (
	// ErrNotFound: no such conversation or agent.
	ErrNotFound = errors.New("chat: not found")
	// ErrConversationClosed: the conversation was reset. The error is a
	// *ClosedError that names its successor.
	ErrConversationClosed = errors.New("chat: conversation closed")
	// ErrUnknownAttachment: no such attachment on this conversation.
	ErrUnknownAttachment = errors.New("chat: unknown attachment")
	// ErrStaleAttachment: the attachment is from an older generation. The
	// error is a *StaleError that carries the current one.
	ErrStaleAttachment = errors.New("chat: stale attachment")
	// ErrNotController: a viewer attachment cannot hold the lease.
	ErrNotController = errors.New("chat: attachment is not a controller")
	// ErrLeaseHeld: another attachment holds an unexpired lease. The error is
	// a *LeaseHeldError.
	ErrLeaseHeld = errors.New("chat: lease held by another attachment")
	// ErrLeaseRequired: the operation needs the input lease.
	ErrLeaseRequired = errors.New("chat: input lease required")
	// ErrBusy: a run is active on the conversation.
	ErrBusy = errors.New("chat: a run is active")
	// ErrRunNotActive: the run to cancel is not the active run.
	ErrRunNotActive = errors.New("chat: run is not active")
	// ErrUnknownApproval: no such approval on the active run.
	ErrUnknownApproval = errors.New("chat: unknown approval")
	// ErrApprovalResolved: the approval was already answered or has expired.
	ErrApprovalResolved = errors.New("chat: approval already resolved")
	// ErrScopeUnavailable: the approval cannot be answered with that scope.
	ErrScopeUnavailable = errors.New("chat: approval scope not available")
	// ErrRequestConflict: a request_id was reused with different content.
	ErrRequestConflict = errors.New("chat: request_id reused with different content")
	// ErrInvalid: the request is malformed. The error is an *InvalidError.
	ErrInvalid = errors.New("chat: invalid request")
	// ErrRejected: the host's Intercept refused the turn. The error is a
	// *RejectedError.
	ErrRejected = errors.New("chat: turn rejected")
	// ErrRunner: the runner failed to start a turn or to take an answer.
	ErrRunner = errors.New("chat: runner error")
	// ErrServiceClosed: the Service was closed.
	ErrServiceClosed = errors.New("chat: service closed")
)

// ClosedError reports a conversation that was reset.
type ClosedError struct {
	ConversationID string
	// Successor is the conversation that replaced it.
	Successor string
}

func (e *ClosedError) Error() string {
	return fmt.Sprintf("chat: conversation %s is closed; its successor is %s", e.ConversationID, e.Successor)
}

// Is matches [ErrConversationClosed].
func (e *ClosedError) Is(target error) bool { return target == ErrConversationClosed }

// StaleError reports an attachment from an older generation.
type StaleError struct {
	// Generation is the conversation's current generation.
	Generation uint64
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("chat: stale attachment; the conversation is at generation %d", e.Generation)
}

// Is matches [ErrStaleAttachment].
func (e *StaleError) Is(target error) bool { return target == ErrStaleAttachment }

// LeaseHeldError reports who holds the lease.
type LeaseHeldError struct{ Lease Lease }

func (e *LeaseHeldError) Error() string {
	return fmt.Sprintf("chat: lease held by attachment %s", e.Lease.Holder)
}

// Is matches [ErrLeaseHeld].
func (e *LeaseHeldError) Is(target error) bool { return target == ErrLeaseHeld }

// InvalidError says what is wrong with a request.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "chat: invalid request: " + e.Reason }

// Is matches [ErrInvalid].
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...)}
}

// RejectedError is a turn the host's Intercept refused.
type RejectedError struct {
	Code    string
	Message string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("chat: turn rejected (%s): %s", e.Code, e.Message)
}

// Is matches [ErrRejected].
func (e *RejectedError) Is(target error) bool { return target == ErrRejected }
