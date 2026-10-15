package ssekit

import (
	"bytes"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// WriterOption configures NewWriter.
type WriterOption func(*writerConfig)

type writerConfig struct {
	headers      [][2]string
	noBufHint    bool
	cacheControl string
}

// WithHeader sets a response header after the defaults, so it can override
// them (including Content-Type). Use it for what only the application knows;
// the library sets no CORS or Connection headers.
func WithHeader(key, value string) WriterOption {
	return func(c *writerConfig) { c.headers = append(c.headers, [2]string{key, value}) }
}

// WithoutBufferingHint drops the default "X-Accel-Buffering: no" header.
func WithoutBufferingHint() WriterOption {
	return func(c *writerConfig) { c.noBufHint = true }
}

// WithCacheControl replaces the default "Cache-Control: no-cache, no-transform".
func WithCacheControl(s string) WriterOption {
	return func(c *writerConfig) { c.cacheControl = s }
}

// Writer writes Server-Sent Events to one response. Every write is flushed and
// every flush or write error is returned to the caller. After the first error
// the Writer is dead and returns that same error. It is safe for concurrent use.
type Writer struct {
	mu  sync.Mutex
	w   http.ResponseWriter
	rc  *http.ResponseController
	err error
}

// NewWriter prepares w for an event stream: it sets the headers, sends status
// 200, clears the connection's write and read deadlines (best effort: a wrapped
// writer that does not support that is tolerated), and flushes so the client
// sees the response immediately. It returns ErrNoFlusher, without touching
// headers, when w cannot flush.
func NewWriter(w http.ResponseWriter, o ...WriterOption) (*Writer, error) {
	if !canFlush(w) {
		return nil, ErrNoFlusher
	}
	cfg := writerConfig{cacheControl: "no-cache, no-transform"}
	for _, f := range o {
		f(&cfg)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", cfg.cacheControl)
	if !cfg.noBufHint {
		h.Set("X-Accel-Buffering", "no")
	}
	for _, kv := range cfg.headers {
		h.Set(kv[0], kv[1])
	}
	rc := http.NewResponseController(w)
	// A server-wide WriteTimeout or ReadTimeout would cut the stream. Errors
	// (http.ErrNotSupported on wrapped or in-memory writers) are tolerated.
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return nil, err
	}
	return &Writer{w: w, rc: rc}, nil
}

func canFlush(w http.ResponseWriter) bool {
	for {
		switch v := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = v.Unwrap()
		default:
			return false
		}
	}
}

// Send writes e as one frame and flushes. Field order on the wire is id,
// event, retry, data. An Event with only Retry set is written as a bare
// "retry:" frame that receivers do not dispatch. Errors from the underlying
// write or flush are returned.
func (w *Writer) Send(e Event) error {
	frame, err := appendFrame(nil, e)
	if err != nil {
		return err
	}
	return w.write(frame)
}

// Comment writes a comment frame (": text") and flushes. A line break inside s
// becomes several comment lines.
func (w *Writer) Comment(s string) error {
	return w.write(appendLines(nil, ": ", []byte(s)))
}

func (w *Writer) write(frame []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	frame = append(frame, '\n')
	if _, err := w.w.Write(frame); err != nil {
		w.err = err
		return err
	}
	if err := w.rc.Flush(); err != nil {
		w.err = err
		return err
	}
	return nil
}

func appendFrame(dst []byte, e Event) ([]byte, error) {
	if bytes.ContainsAny([]byte(e.ID), "\r\n\x00") || bytes.ContainsAny([]byte(e.Name), "\r\n") || e.Retry < 0 {
		return nil, ErrInvalidField
	}
	if e.ID != "" {
		dst = append(dst, "id: "...)
		dst = append(dst, e.ID...)
		dst = append(dst, '\n')
	}
	if e.Name != "" {
		dst = append(dst, "event: "...)
		dst = append(dst, e.Name...)
		dst = append(dst, '\n')
	}
	ms := e.Retry.Milliseconds()
	if ms > 0 {
		dst = append(dst, "retry: "...)
		dst = strconv.AppendInt(dst, ms, 10)
		dst = append(dst, '\n')
	}
	if ms > 0 && e.ID == "" && e.Name == "" && len(e.Data) == 0 {
		return dst, nil
	}
	return appendLines(dst, "data: ", e.Data), nil
}

// appendLines writes s as one prefixed line per CRLF, LF or lone CR separated
// segment. A trailing terminator yields a final empty line, so the receiver
// reassembles s exactly.
func appendLines(dst []byte, prefix string, s []byte) []byte {
	for {
		i := bytes.IndexAny(s, "\r\n")
		if i < 0 {
			dst = append(dst, prefix...)
			dst = append(dst, s...)
			return append(dst, '\n')
		}
		dst = append(dst, prefix...)
		dst = append(dst, s[:i]...)
		dst = append(dst, '\n')
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			i++
		}
		s = s[i+1:]
	}
}
