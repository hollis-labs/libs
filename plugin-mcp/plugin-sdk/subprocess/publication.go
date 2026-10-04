package subprocess

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const DefaultQueueFrames = 32
const DefaultQueuedWriteBytes = 8 * 1024 * 1024
const TerminalCreditBytes = 1024
const controlBurstLimit = 4

var errPublicationFull = errors.New("subprocess: publication capacity exceeded")

// QueueLimits narrow each independent writer lane. Queued bytes can reach twice
// Bytes, plus one in-progress frame bounded by FrameLimits.OutputBytes.
type QueueLimits struct{ Frames, Bytes int }

func queueLimits(v QueueLimits) (QueueLimits, error) {
	if v.Frames == 0 {
		v.Frames = DefaultQueueFrames
	}
	if v.Bytes == 0 {
		v.Bytes = DefaultQueuedWriteBytes
	}
	if v.Frames < 1 || v.Frames > DefaultQueueFrames || v.Bytes < 1 || v.Bytes > DefaultQueuedWriteBytes {
		return v, errors.New("subprocess: queue limits must be positive and may only narrow defaults")
	}
	return v, nil
}

type publication struct {
	frame   []byte
	receipt func(error)
	credit  *terminalCredit
}
type writerLane struct {
	queue                                []publication
	bytes, reservedFrames, reservedBytes int
}

// A credit reserves capacity before execution. It is owned by one writer and
// consumed once by a terminal publication, or released after rejected admission.
type terminalCredit struct {
	writer *frameWriter
	bytes  int
	state  uint8
}

func (c *terminalCredit) release() {
	if c == nil {
		return
	}
	w := c.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	if c.state == 0 {
		w.control.reservedFrames--
		w.control.reservedBytes -= c.bytes
		c.state = 2
	}
}

// Admission never waits; receipts report whole-write completion. Handler limits
// and cancellation live above this writer, which retains no waiter backlog.
type frameWriter struct {
	mu                sync.Mutex
	ordinary, control writerLane
	limits            QueueLimits
	burst             int
	activeBytes       int
	wake              chan struct{}
	done, aborted     chan struct{}
	sealed            bool
	err               error
	onFailure         func(error)
}

func newFrameWriter(out io.Writer, timeout time.Duration, interrupt func()) *frameWriter {
	return newFrameWriterWithLimits(out, timeout, interrupt, QueueLimits{DefaultQueueFrames, DefaultQueuedWriteBytes})
}
func newFrameWriterWithLimits(out io.Writer, timeout time.Duration, interrupt func(), limits QueueLimits) *frameWriter {
	w := &frameWriter{limits: limits, wake: make(chan struct{}, 1), done: make(chan struct{}), aborted: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			item, ok, finished := w.take()
			if finished {
				return
			}
			if !ok {
				<-w.wake
				continue
			}
			result := make(chan error, 1)
			go func() {
				n, err := out.Write(item.frame)
				if err == nil && n != len(item.frame) {
					err = io.ErrShortWrite
				}
				result <- err
			}()
			timer := time.NewTimer(timeout)
			var err error
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
			w.mu.Lock()
			w.activeBytes = 0
			if item.credit != nil {
				item.credit.state = 2
			}
			w.mu.Unlock()
			if item.receipt != nil {
				item.receipt(err)
			}
		}
	}()
	return w
}
func (w *frameWriter) notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *frameWriter) take() (publication, bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	lane := &w.control
	if len(lane.queue) == 0 || (w.burst >= controlBurstLimit && len(w.ordinary.queue) > 0) {
		lane = &w.ordinary
	}
	if len(lane.queue) == 0 {
		return publication{}, false, w.sealed
	}
	item := lane.queue[0]
	lane.queue[0] = publication{}
	lane.queue = lane.queue[1:]
	lane.bytes -= len(item.frame)
	if lane == &w.control {
		if w.burst < controlBurstLimit {
			w.burst++
		}
	} else {
		w.burst = 0
	}
	w.activeBytes = len(item.frame)
	return item, true, false
}
func (w *frameWriter) reserveTerminal(bytes int) (*terminalCredit, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return nil, w.err
	}
	if w.sealed {
		return nil, errConnectionClosed
	}
	lane := &w.control
	if bytes < 1 || len(lane.queue)+lane.reservedFrames >= w.limits.Frames || bytes > w.limits.Bytes-lane.bytes-lane.reservedBytes {
		return nil, errPublicationFull
	}
	lane.reservedFrames++
	lane.reservedBytes += bytes
	return &terminalCredit{writer: w, bytes: bytes}, nil
}
func (w *frameWriter) submit(frame []byte, receipt func(error)) error {
	return w.enqueue(frame, receipt, false, nil)
}
func (w *frameWriter) submitControl(frame []byte, receipt func(error)) error {
	return w.enqueue(frame, receipt, true, nil)
}
func (w *frameWriter) submitTerminal(frame []byte, credit *terminalCredit, receipt func(error)) error {
	return w.enqueue(frame, receipt, true, credit)
}
func (w *frameWriter) enqueue(frame []byte, receipt func(error), control bool, credit *terminalCredit) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if w.sealed {
		return errConnectionClosed
	}
	lane := &w.ordinary
	if control {
		lane = &w.control
	}
	frames, bytes := len(lane.queue)+lane.reservedFrames, lane.bytes+lane.reservedBytes
	if credit != nil {
		if !control || credit.writer != w || credit.state != 0 {
			return errPublicationFull
		}
		frames--
		bytes -= credit.bytes
	}
	if frames >= w.limits.Frames || len(frame) > w.limits.Bytes-bytes {
		return errPublicationFull
	}
	if credit != nil {
		lane.reservedFrames--
		lane.reservedBytes -= credit.bytes
		credit.state = 1
	}
	lane.queue = append(lane.queue, publication{frame, receipt, credit})
	lane.bytes += len(frame)
	w.notify()
	return nil
}
func (w *frameWriter) seal() { w.mu.Lock(); w.sealed = true; w.mu.Unlock(); w.notify() }
func (w *frameWriter) abort(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
		close(w.aborted)
	}
	w.sealed = true
	queued := append(w.ordinary.queue, w.control.queue...)
	w.ordinary.queue = nil
	w.control.queue = nil
	w.ordinary.bytes = 0
	w.control.bytes = 0
	for _, item := range queued {
		if item.credit != nil {
			item.credit.state = 2
		}
	}
	w.mu.Unlock()
	w.notify()
	for _, item := range queued {
		if item.receipt != nil {
			item.receipt(err)
		}
	}
}
func (w *frameWriter) failure() error { w.mu.Lock(); defer w.mu.Unlock(); return w.err }
