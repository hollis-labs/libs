package redact

import (
	"errors"
	"io"
	"strings"
)

// Defaults for [Redactor.Writer].
const (
	DefaultMaxPending = 64 << 10 // longest unterminated line held before a forced flush
	DefaultHold       = 4 << 10  // bytes kept back across a forced flush
)

// WriterOption configures [Redactor.Writer].
type WriterOption func(*redactingWriter)

// WithMaxPending sets how many bytes the writer holds waiting for a newline
// before it flushes anyway (never below 2 * the hold).
func WithMaxPending(n int) WriterOption { return func(w *redactingWriter) { w.maxPending = n } }

// WithHold sets how many trailing bytes a forced flush keeps back, so a value that
// straddles the flush point is still seen whole. It should exceed the longest value
// you expect on a single unbroken line.
func WithHold(n int) WriterOption { return func(w *redactingWriter) { w.hold = n } }

// Writer returns a streaming redactor in front of dst. It emits complete lines, so
// every rule and every known value is matched on whole text however the input was
// split across Write calls; the output equals r.Text of the whole stream. A match
// still open at the end of the buffered text (an unterminated private-key block, a
// value whose line has not ended) is held until it closes.
//
// Memory is bounded: a line longer than MaxPending is flushed anyway, keeping Hold
// bytes back; a value longer than Hold that straddles such a flush can be missed,
// so size the two for the data. Close flushes the rest and does not close dst.
// The writer is not safe for concurrent use.
func (r *Redactor) Writer(dst io.Writer, opts ...WriterOption) io.WriteCloser {
	if r == nil {
		r = Default()
	}
	w := &redactingWriter{r: r, dst: dst, maxPending: DefaultMaxPending, hold: DefaultHold}
	for _, o := range opts {
		o(w)
	}
	if w.hold < 0 {
		w.hold = 0
	}
	if w.maxPending < 2*w.hold || w.maxPending < 1 {
		w.maxPending = max(2*w.hold, 1)
	}
	return w
}

type redactingWriter struct {
	r          *Redactor
	dst        io.Writer
	maxPending int
	hold       int
	pending    []byte
	closed     bool
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("redact: write to closed Writer")
	}
	if w.dst == nil {
		return 0, errors.New("redact: Writer destination is nil")
	}
	w.pending = append(w.pending, p...)
	if err := w.drain(false); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *redactingWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.dst == nil {
		return errors.New("redact: Writer destination is nil")
	}
	err := w.drain(true)
	w.pending = nil
	return err
}

// drain emits what is safe to emit. final emits everything.
func (w *redactingWriter) drain(final bool) error {
	if len(w.pending) == 0 {
		return nil
	}
	s := string(w.pending)
	if final {
		return writeAll(w.dst, w.r.Text(s))
	}
	cut := strings.LastIndexByte(s, '\n') + 1
	forced := false
	if len(s)-cut > w.maxPending {
		cut = len(s) - w.hold
		forced = true
	}
	if cut <= 0 {
		return nil
	}
	fs := w.r.Scan(s)
	for changed := true; changed; {
		changed = false
		for _, f := range fs {
			// A finding that crosses the cut, or that runs to the end of the buffer
			// and so may still grow (an unterminated private-key block), is held whole.
			open := f.End > cut || f.End == len(s) || (forced && f.End == cut)
			if f.Start < cut && open {
				cut = f.Start
				changed = true
			}
		}
		if n := w.r.scope.heldNewlinePrefix(s[:cut]); n > 0 {
			cut -= n
			changed = true
		}
		if cut <= 0 {
			return nil
		}
	}
	var b strings.Builder
	pos := 0
	for _, f := range fs {
		if f.Start >= cut {
			break
		}
		if s[f.Start:f.End] == w.r.marker {
			continue
		}
		b.WriteString(s[pos:f.Start])
		b.WriteString(w.r.marker)
		pos = f.End
	}
	b.WriteString(s[pos:cut])
	if err := writeAll(w.dst, b.String()); err != nil {
		return err
	}
	w.pending = append(w.pending[:0], w.pending[cut:]...)
	return nil
}

func writeAll(dst io.Writer, s string) error {
	b := []byte(s)
	for len(b) > 0 {
		n, err := dst.Write(b)
		if n < 0 || n > len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
