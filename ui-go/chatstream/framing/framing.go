package framing

import (
	"bufio"
	"bytes"
	"io"
	"iter"

	chatstream "github.com/hollis-labs/go-chatstream"
	ssekit "github.com/hollis-labs/go-ssekit"
)

// SSE yields one Frame per server-sent event read from r: Event is the event
// name, Data the joined data lines, ID the last event id. A read error ends the
// sequence with it; a clean EOF ends it without one (ssekit discards an
// unterminated event, as the specification requires).
func SSE(r io.Reader, opts ...ssekit.ReadOption) iter.Seq2[chatstream.Frame, error] {
	return func(yield func(chatstream.Frame, error) bool) {
		for ev, err := range ssekit.Read(r, opts...) {
			if err != nil {
				yield(chatstream.Frame{}, err)
				return
			}
			if !yield(chatstream.Frame{Event: ev.Name, Data: ev.Data, ID: ev.ID}, nil) {
				return
			}
		}
	}
}

// DefaultMaxLine is the longest line Lines accepts.
const DefaultMaxLine = 16 << 20

// Lines yields one Frame per non-blank line of r, with the line ending (LF or
// CRLF) removed and the bytes otherwise untouched. A line longer than
// maxLine (DefaultMaxLine when <= 0) ends the sequence with bufio.ErrTooLong. A
// final line without a newline is a frame, since a process that exits after
// writing its last JSON value without a newline has still written it.
func Lines(r io.Reader, maxLine int) iter.Seq2[chatstream.Frame, error] {
	if maxLine <= 0 {
		maxLine = DefaultMaxLine
	}
	return func(yield func(chatstream.Frame, error) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, min(64<<10, maxLine)), maxLine) // the cap is max(initial capacity, maxLine)
		for sc.Scan() {
			line := bytes.TrimRight(sc.Bytes(), "\r")
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if !yield(chatstream.Frame{Data: append([]byte(nil), line...)}, nil) {
				return
			}
		}
		if err := sc.Err(); err != nil {
			yield(chatstream.Frame{}, err)
		}
	}
}
