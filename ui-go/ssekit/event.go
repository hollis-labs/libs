package ssekit

import (
	"errors"
	"fmt"
	"time"
)

// Event is one Server-Sent Event. The library never looks inside Data and
// never interprets ID: both are opaque to it.
type Event struct {
	// ID is the event's id: field. On the writing side "" means no id: line is
	// written, so a control frame does not clobber the client's stored
	// Last-Event-ID. On the reading side ID is the stream's current
	// last-event-ID as the WHATWG algorithm defines it: it persists across
	// events, so an event without its own id: carries the previous one.
	ID string
	// Name is the event: field. "" means the spec default, "message". The reader
	// leaves Name empty for an event with no (or an empty) event: field.
	Name string
	// Data is the payload. On the writing side a "\n", "\r\n" or lone "\r"
	// inside it becomes a line break between several data: lines, and the reader
	// joins them back with "\n", so a payload round-trips byte for byte. The
	// bytes returned by the reader are owned by the caller.
	Data []byte
	// Retry is the reconnection delay. On the writing side 0 omits the retry:
	// field (it is written in whole milliseconds). On the reading side it is the
	// most recent valid retry: value seen since the previous event, or 0.
	Retry time.Duration
}

var (
	// ErrNoFlusher is returned by NewWriter when the ResponseWriter cannot
	// flush; the response headers have not been touched.
	ErrNoFlusher = errors.New("ssekit: ResponseWriter does not support flushing")
	// ErrInvalidField is returned when an Event ID or Name contains a CR or LF
	// (or, for the ID, a NUL, which receivers ignore): written as-is it would
	// let the value inject frames.
	ErrInvalidField = errors.New("ssekit: id or event name contains CR, LF or NUL")
	// ErrEventTooLarge is returned by the reader when one event block exceeds
	// the configured size limit.
	ErrEventTooLarge = errors.New("ssekit: event exceeds size limit")
	// ErrIdle is the cause of a reconnect after the client saw no bytes at all
	// (not even comments) for the idle timeout.
	ErrIdle = errors.New("ssekit: stream idle timeout")
	// ErrNotEventStream is yielded by the client when a 200 response does not
	// carry Content-Type text/event-stream. It is final: retrying cannot help.
	ErrNotEventStream = errors.New("ssekit: response is not text/event-stream")
)

// StatusError is yielded by the client for a non-200 response that the
// IsFinalStatus policy treats as final.
type StatusError struct {
	Code   int
	Status string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("ssekit: server answered %s", e.Status)
}

// GapError is yielded by the client when the continuity predicate rejects the
// step from one event id to the next. The stream stops: a gap means the
// consumer missed events and must reconcile before it trusts the rest.
type GapError struct {
	// Prev is the last event id delivered before the gap, Next the id of the
	// event that arrived instead of the expected one.
	Prev, Next string
	// Event is the event that revealed the gap. It was NOT delivered.
	Event Event
}

func (e *GapError) Error() string {
	return fmt.Sprintf("ssekit: gap in event stream between id %q and %q", e.Prev, e.Next)
}
