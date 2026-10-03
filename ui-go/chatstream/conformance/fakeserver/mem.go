package fakeserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// memTransport is an http.RoundTripper that runs a handler in-process and
// streams its response body through an in-memory buffer. There are no sockets,
// so it works inside a testing/synctest bubble; the buffer is unbounded and
// writes never block, as a socket's kernel buffer does for a few events, so a
// scripted connection completes its writes even if the client has stopped reading.
type memTransport struct{ h http.Handler }

func (m memTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	bp := newBufPipe()
	w := &memWriter{hdr: http.Header{}, bp: bp, ready: make(chan struct{}), status: http.StatusOK}

	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				if err, ok := r.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
					panic(r)
				}
				// An aborted handler (ssetest's Drop) ends the response without its
				// proper end: after what was written, the reader sees an unexpected EOF.
				w.WriteHeader(http.StatusOK)
				bp.closeWriter(io.ErrUnexpectedEOF)
				return
			}
			w.WriteHeader(http.StatusOK)
			if err := req.Context().Err(); err != nil {
				// The client gave up: it sees why, not a clean end, whichever of
				// the handler returning and the watcher below gets there first.
				bp.abort(err)
				return
			}
			bp.closeWriter(io.EOF)
		}()
		m.h.ServeHTTP(w, req.WithContext(ctx))
	}()

	select {
	case <-w.ready:
	case <-req.Context().Done():
		cancel()
		bp.abort(req.Context().Err())
		return nil, req.Context().Err()
	}
	// A canceled request context ends a blocked read with its error, as net/http does.
	go func() {
		select {
		case <-req.Context().Done():
			bp.abort(req.Context().Err())
		case <-ctx.Done():
		}
	}()
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		StatusCode:    w.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        w.hdr,
		Body:          &memBody{bp: bp, cancel: cancel},
		ContentLength: -1,
		Request:       req,
	}, nil
}

type memWriter struct {
	hdr    http.Header
	bp     *bufPipe
	once   sync.Once
	ready  chan struct{}
	status int
}

func (w *memWriter) Header() http.Header { return w.hdr }

func (w *memWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.status = code
		close(w.ready)
	})
}

func (w *memWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.bp.write(b)
}

// Flush marks the response started, as a real server's flush sends the headers.
func (w *memWriter) Flush() { w.WriteHeader(http.StatusOK) }

// memBody closes the response: the handler's context is canceled so a step
// waiting on the client (a stall) returns.
type memBody struct {
	bp     *bufPipe
	cancel context.CancelFunc
}

func (b *memBody) Read(p []byte) (int, error) { return b.bp.read(p) }

func (b *memBody) Close() error {
	b.cancel()
	b.bp.closeReader()
	return nil
}

// bufPipe is an unbounded in-memory pipe. Reads return buffered bytes first and
// then the writer's close error; sync.Cond waits are durably blocking, so a
// synctest bubble can advance time while a reader waits.
type bufPipe struct {
	mu      sync.Mutex
	cond    *sync.Cond
	buf     []byte
	werr    error // set once the writer is done
	rclosed bool
}

func newBufPipe() *bufPipe {
	p := &bufPipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *bufPipe) write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rclosed {
		return 0, io.ErrClosedPipe
	}
	if p.werr != nil {
		return 0, io.ErrClosedPipe
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *bufPipe) read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && p.werr == nil && !p.rclosed {
		p.cond.Wait()
	}
	if p.rclosed {
		return 0, io.ErrClosedPipe
	}
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return 0, p.werr
}

// closeWriter ends the stream after what is buffered; the reader then gets err.
func (p *bufPipe) closeWriter(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.werr == nil {
		p.werr = err
	}
	p.cond.Broadcast()
}

// abort fails the stream at once: buffered bytes are dropped and the reader gets err.
func (p *bufPipe) abort(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = nil
	if p.werr == nil || errors.Is(p.werr, io.EOF) {
		p.werr = err
	}
	p.cond.Broadcast()
}

func (p *bufPipe) closeReader() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rclosed = true
	p.cond.Broadcast()
}
