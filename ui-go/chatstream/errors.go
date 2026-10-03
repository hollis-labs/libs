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
	// ErrFrameTooLarge is wrapped by the framing layer's error for one frame
	// (an SSE event or a line) over its size limit. A decoder closed with it ends
	// the run with CodeFrameTooLarge, not retryable: the same frame would be sent
	// again.
	ErrFrameTooLarge = errors.New("chatstream: frame exceeds the size limit")
)
