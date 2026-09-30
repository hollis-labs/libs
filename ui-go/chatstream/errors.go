package chatstream

import "errors"

// Sentinel errors.
var (
	// ErrInvalidEvent is wrapped by errors Reduce returns for an event that does
	// not fit the stream it is applied to.
	ErrInvalidEvent = errors.New("chatstream: invalid event")
	// ErrAfterTerminal is wrapped when an event follows the run's terminal event.
	ErrAfterTerminal = errors.New("chatstream: event after terminal event")
	// ErrDecoderClosed is returned by Decoder.Decode after Close.
	ErrDecoderClosed = errors.New("chatstream: decoder is closed")
)
