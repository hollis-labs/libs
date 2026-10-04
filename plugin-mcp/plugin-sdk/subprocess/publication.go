package subprocess

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type publication struct {
	frame   []byte
	receipt func(error)
}

// No scheduler or waiter queue: saturated core publication fails closed.
type frameWriter struct {
	mu        sync.Mutex
	queue     chan publication
	done      chan struct{}
	aborted   chan struct{}
	sealed    bool
	err       error
	onFailure func(error)
}

func newFrameWriter(out io.Writer, timeout time.Duration, interrupt func()) *frameWriter {
	w := &frameWriter{queue: make(chan publication, 32), done: make(chan struct{}), aborted: make(chan struct{})}
	go func() {
		defer close(w.done)
		for item := range w.queue {
			w.mu.Lock()
			err := w.err
			w.mu.Unlock()
			if err == nil {
				result := make(chan error, 1)
				go func() {
					n, e := out.Write(item.frame)
					if e == nil && n != len(item.frame) {
						e = io.ErrShortWrite
					}
					result <- e
				}()
				timer := time.NewTimer(timeout)
				select {
				case err = <-result:
				case <-w.aborted:
					err = w.failure()
					interrupt()
				case <-timer.C:
					err = ErrWriteTimeout
					interrupt()
				}
				timer.Stop()
				if err != nil {
					err = fmt.Errorf("stdout: %w", err)
					w.abort(err)
					if w.onFailure != nil {
						w.onFailure(err)
					}
				}
			}
			if item.receipt != nil {
				item.receipt(err)
			}
		}
	}()
	return w
}
func (w *frameWriter) submit(frame []byte, receipt func(error)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if w.sealed {
		return errConnectionClosed
	}
	select {
	case w.queue <- publication{frame, receipt}:
		return nil
	default:
		return errCorrelation
	}
}
func (w *frameWriter) seal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.sealed {
		w.sealed = true
		close(w.queue)
	}
}
func (w *frameWriter) abort(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
		close(w.aborted)
	}
	if !w.sealed {
		w.sealed = true
		close(w.queue)
	}
}
func (w *frameWriter) failure() error { w.mu.Lock(); defer w.mu.Unlock(); return w.err }
