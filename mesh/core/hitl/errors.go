package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Sentinel errors; each typed error unwraps to one, so errors.Is works.
var (
	// ErrStaleRevision: an expected revision no longer matches.
	ErrStaleRevision = errors.New("hitl: stale revision")
	// ErrIdempotencyConflict: an idempotency key was reused with different content.
	ErrIdempotencyConflict = errors.New("hitl: idempotency conflict")
	// ErrTerminalConflict: the item already has a terminal outcome that
	// differs from what the operation would have produced.
	ErrTerminalConflict = errors.New("hitl: terminal conflict")
	// ErrNotFound: no such item for this caller. It is also what a caller
	// outside the item's scope receives, so existence does not leak.
	ErrNotFound = errors.New("hitl: not found")
	// ErrInvalidRequest: the command or request failed validation.
	ErrInvalidRequest = errors.New("hitl: invalid request")
	// ErrUnauthorized: the caller is not allowed (used by DecodeError for
	// implementations that distinguish it from ErrNotFound).
	ErrUnauthorized = errors.New("hitl: unauthorized")
)

// StaleRevisionError reports an operation refused because the item's revision
// moved. If the item is terminal, Outcome carries it.
type StaleRevisionError struct {
	Operation    string // "resolve" or "withdraw"
	ItemID       string
	RevisionKind string // "interaction" (withdraw: only this) or "presented_projection"
	Expected     int64
	Actual       int64
	CurrentState State
	Outcome      Outcome
}

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("%v: %s %s revision for item %q: expected %d, actual %d",
		ErrStaleRevision, e.Operation, e.RevisionKind, e.ItemID, e.Expected, e.Actual)
}

// Unwrap returns ErrStaleRevision.
func (e *StaleRevisionError) Unwrap() error { return ErrStaleRevision }

// IdempotencyConflictError reports a reused idempotency key with different
// immutable content; ExistingItemID names the item that owns the key.
type IdempotencyConflictError struct {
	IdempotencyKey string
	ExistingItemID string
}

func (e *IdempotencyConflictError) Error() string {
	return fmt.Sprintf("%v: key %q already belongs to item %q", ErrIdempotencyConflict, e.IdempotencyKey, e.ExistingItemID)
}

// Unwrap returns ErrIdempotencyConflict.
func (e *IdempotencyConflictError) Unwrap() error { return ErrIdempotencyConflict }

// TerminalConflictError reports that a withdraw or resolve lost to another
// terminal outcome. Outcome is the existing, immutable one.
type TerminalConflictError struct {
	Outcome Outcome
}

func (e *TerminalConflictError) Error() string {
	if e.Outcome == nil {
		return ErrTerminalConflict.Error()
	}
	return fmt.Sprintf("%v: item %q is already %s", ErrTerminalConflict, e.Outcome.OutcomeItemID(), e.Outcome.OutcomeState())
}

// Unwrap returns ErrTerminalConflict.
func (e *TerminalConflictError) Unwrap() error { return ErrTerminalConflict }

// CodedError is an error the wire carries as just a code and a message
// (not_found, unauthorized, validation_failed, wait_canceled, await_timeout,
// hitl_error, ...).
type CodedError struct {
	Code    string
	Message string
}

func (e *CodedError) Error() string {
	if e.Message == "" {
		return "hitl: " + e.Code
	}
	return "hitl: " + e.Code + ": " + e.Message
}

// Unwrap maps well-known codes onto the sentinel errors.
func (e *CodedError) Unwrap() error {
	switch e.Code {
	case "not_found":
		return ErrNotFound
	case "validation_failed":
		return ErrInvalidRequest
	case "unauthorized":
		return ErrUnauthorized
	}
	return nil
}

var (
	_ error = (*StaleRevisionError)(nil)
	_ error = (*IdempotencyConflictError)(nil)
	_ error = (*TerminalConflictError)(nil)
	_ error = (*CodedError)(nil)
)

type errorWire struct {
	ContractVersion string          `json:"contract_version"`
	Code            string          `json:"code"`
	Message         string          `json:"message,omitempty"`
	Operation       string          `json:"operation,omitempty"`
	ItemID          string          `json:"item_id,omitempty"`
	RevisionKind    string          `json:"revision_kind,omitempty"`
	Expected        int64           `json:"expected_revision,omitempty"`
	Actual          int64           `json:"actual_revision,omitempty"`
	CurrentState    State           `json:"current_state,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	ExistingItemID  string          `json:"existing_item_id,omitempty"`
	TerminalOutcome json.RawMessage `json:"terminal_outcome,omitempty"`
}

func decodeOptionalOutcome(raw json.RawMessage) (Outcome, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	return UnmarshalOutcome(raw)
}

// DecodeError turns a wire error body ({"code": ...}) into its typed error:
// *StaleRevisionError, *IdempotencyConflictError, *TerminalConflictError, or a
// *CodedError for any other code. A body that is not a JSON object with a
// code yields an error wrapping ErrInvalidRequest.
func DecodeError(body []byte) error {
	var w errorWire
	if err := json.Unmarshal(body, &w); err != nil {
		return fmt.Errorf("%w: undecodable error body: %w", ErrInvalidRequest, err)
	}
	if w.Code == "" {
		return fmt.Errorf("%w: error body has no code", ErrInvalidRequest)
	}
	switch w.Code {
	case "stale_revision":
		o, err := decodeOptionalOutcome(w.TerminalOutcome)
		if err != nil {
			return err
		}
		return &StaleRevisionError{
			Operation: w.Operation, ItemID: w.ItemID, RevisionKind: w.RevisionKind,
			Expected: w.Expected, Actual: w.Actual, CurrentState: w.CurrentState, Outcome: o,
		}
	case "idempotency_conflict":
		return &IdempotencyConflictError{IdempotencyKey: w.IdempotencyKey, ExistingItemID: w.ExistingItemID}
	case "terminal_conflict":
		o, err := decodeOptionalOutcome(w.TerminalOutcome)
		if err != nil {
			return err
		}
		if o == nil {
			return fmt.Errorf("%w: terminal_conflict without terminal_outcome", ErrInvalidRequest)
		}
		return &TerminalConflictError{Outcome: o}
	}
	return &CodedError{Code: w.Code, Message: w.Message}
}

// EncodeError renders err as a wire error body, the inverse of DecodeError.
// Typed errors keep their payload; validation, not-found and unauthorized
// errors get their codes; anything else becomes code "hitl_error".
func EncodeError(err error) []byte {
	w := errorWire{ContractVersion: ContractVersion, Code: "hitl_error", Message: err.Error()}
	var stale *StaleRevisionError
	var idem *IdempotencyConflictError
	var term *TerminalConflictError
	var coded *CodedError
	switch {
	case errors.As(err, &stale):
		w.Code, w.Operation, w.ItemID, w.RevisionKind = "stale_revision", stale.Operation, stale.ItemID, stale.RevisionKind
		w.Expected, w.Actual, w.CurrentState = stale.Expected, stale.Actual, stale.CurrentState
		if stale.Outcome != nil {
			w.TerminalOutcome, _ = json.Marshal(stale.Outcome)
		}
	case errors.As(err, &idem):
		w.Code, w.IdempotencyKey, w.ExistingItemID = "idempotency_conflict", idem.IdempotencyKey, idem.ExistingItemID
	case errors.As(err, &term) && term.Outcome != nil:
		w.Code = "terminal_conflict"
		w.TerminalOutcome, _ = json.Marshal(term.Outcome)
	case errors.As(err, &coded):
		w.Code = coded.Code
	case errors.Is(err, ErrNotFound):
		w.Code = "not_found"
	case errors.Is(err, ErrUnauthorized):
		w.Code = "unauthorized"
	case errors.Is(err, ErrInvalidRequest):
		w.Code = "validation_failed"
	case errors.Is(err, context.Canceled):
		w.Code = "wait_canceled"
	}
	b, mErr := json.Marshal(w)
	if mErr != nil {
		return []byte(`{"contract_version":"1.0","code":"hitl_error"}`)
	}
	return b
}

// NewInvalidRequest wraps err so that it satisfies errors.Is(_, ErrInvalidRequest).
// Transports use it for a body that cannot even be decoded.
func NewInvalidRequest(err error) error { return fmt.Errorf("%w: %w", ErrInvalidRequest, err) }
