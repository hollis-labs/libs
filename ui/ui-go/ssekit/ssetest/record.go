package ssetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// Frame is one chunk of a stream and when it arrived, measured from the start
// of the recording.
type Frame struct {
	At  time.Duration
	Raw []byte
}

// Record reads r until EOF and returns one Frame per successful Read, so the
// chunk boundaries and their timing survive. A read error other than io.EOF is
// returned together with the frames recorded so far.
func Record(r io.Reader) ([]Frame, error) {
	start := time.Now()
	var frames []Frame
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			frames = append(frames, Frame{At: time.Since(start), Raw: bytes.Clone(buf[:n])})
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return frames, nil
			}
			return frames, err
		}
	}
}

// Replay writes the frames to w at their recorded offsets from the call, and
// flushes after each one if w can flush (an http.ResponseWriter, or anything
// with Flush() or Flush() error). It stops early with ctx's error.
func Replay(ctx context.Context, w io.Writer, frames []Frame) error {
	start := time.Now()
	for _, f := range frames {
		if wait := f.At - time.Since(start); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			}
		} else if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := w.Write(f.Raw); err != nil {
			return err
		}
		switch fl := w.(type) {
		case interface{ FlushError() error }:
			if err := fl.FlushError(); err != nil {
				return err
			}
		case http.Flusher:
			fl.Flush()
		case interface{ Flush() error }:
			if err := fl.Flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Concat joins the frames' bytes, dropping the timing.
func Concat(frames []Frame) []byte {
	var b []byte
	for _, f := range frames {
		b = append(b, f.Raw...)
	}
	return b
}
