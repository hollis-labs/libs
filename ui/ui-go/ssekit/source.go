package ssekit

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Source produces the events of one stream. Next blocks until an event is
// available. It returns io.EOF when the stream is over and ctx.Err() (or any
// error) when ctx is done; it must return promptly once ctx is canceled.
// A Source is consumed by one goroutine at a time.
//
// The seam is deliberately tiny so an application can adapt a channel, a
// pub/sub subscription or a database tail in a few lines.
type Source interface {
	Next(ctx context.Context) (Event, error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(ctx context.Context) (Event, error)

// Next calls f.
func (f SourceFunc) Next(ctx context.Context) (Event, error) { return f(ctx) }

// ChanSource adapts a channel. A closed channel ends the stream with io.EOF.
func ChanSource(ch <-chan Event) Source {
	return SourceFunc(func(ctx context.Context) (Event, error) {
		select {
		case e, ok := <-ch:
			if !ok {
				return Event{}, io.EOF
			}
			return e, nil
		case <-ctx.Done():
			return Event{}, ctx.Err()
		}
	})
}

// Merge fans several live sources into one. Events from different sources are
// interleaved in arrival order; nothing is buffered or replayed, and no order
// across sources is promised. The merged source ends with io.EOF once every
// input has, and fails with the first non-EOF error any input returns.
//
// Merge starts one goroutine per input on the first call to Next, bound to that
// call's context: it is meant to be consumed through one context (as Serve
// does), and its goroutines end when that context is canceled or every input
// has finished.
func Merge(srcs ...Source) Source {
	return &merged{srcs: srcs}
}

type mergeResult struct {
	ev  Event
	err error // nil for an event, io.EOF for one input finishing
}

type merged struct {
	srcs  []Source
	once  sync.Once
	ch    chan mergeResult
	live  int
	final error
}

func (m *merged) start(ctx context.Context) {
	m.ch = make(chan mergeResult)
	m.live = len(m.srcs)
	for _, s := range m.srcs {
		go func() {
			for {
				ev, err := s.Next(ctx)
				select {
				case m.ch <- mergeResult{ev, err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
}

func (m *merged) Next(ctx context.Context) (Event, error) {
	m.once.Do(func() { m.start(ctx) })
	for {
		if m.final != nil {
			return Event{}, m.final
		}
		if m.live == 0 {
			m.final = io.EOF
			return Event{}, io.EOF
		}
		select {
		case r := <-m.ch:
			switch {
			case r.err == nil:
				return r.ev, nil
			case errors.Is(r.err, io.EOF):
				m.live--
			default:
				m.final = r.err
			}
		case <-ctx.Done():
			return Event{}, ctx.Err()
		}
	}
}

// PollSource turns a poll function into a Source. poll is called with the
// cursor it returned last time ("" on the first call) and returns the events
// found since, plus the new cursor. When a poll finds nothing, the next one is
// made after every (one second if every <= 0). The first poll is immediate. A
// poll error ends the stream with that error. The cursor is opaque here; the
// caller decides what it means.
func PollSource(poll func(ctx context.Context, cursor string) ([]Event, string, error), every time.Duration) Source {
	if every <= 0 {
		every = time.Second
	}
	var (
		cursor string
		queue  []Event
	)
	return SourceFunc(func(ctx context.Context) (Event, error) {
		for {
			if len(queue) > 0 {
				e := queue[0]
				queue = queue[1:]
				return e, nil
			}
			evs, next, err := poll(ctx, cursor)
			if err != nil {
				return Event{}, err
			}
			cursor = next
			if len(evs) > 0 {
				queue = evs
				continue
			}
			t := time.NewTimer(every)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return Event{}, ctx.Err()
			}
		}
	})
}
