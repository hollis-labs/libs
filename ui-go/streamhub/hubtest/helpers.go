package hubtest

import (
	"context"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	streamhub "github.com/hollis-labs/go-streamhub"
)

// failTimeout bounds how long a helper waits before failing the test. It is
// a safety net, never a synchronization mechanism.
const failTimeout = 30 * time.Second

// Next reads one item from sub, failing the test on an error or when nothing
// arrives within a generous timeout.
func Next(t testing.TB, sub streamhub.Subscription) streamhub.Item {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), failTimeout)
	defer cancel()
	it, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return it
}

// Drain reads n items from sub, failing the test on any error.
func Drain(t testing.TB, sub streamhub.Subscription, n int) []streamhub.Item {
	t.Helper()
	out := make([]streamhub.Item, 0, n)
	for range n {
		out = append(out, Next(t, sub))
	}
	return out
}

// Seqs returns the Seq of each record item; a gap item fails the test.
func Seqs(t testing.TB, items []streamhub.Item) []streamhub.Seq {
	t.Helper()
	out := make([]streamhub.Seq, 0, len(items))
	for _, it := range items {
		if it.Gap != nil {
			t.Fatalf("unexpected gap item: %+v", *it.Gap)
		}
		out = append(out, it.Record.Seq)
	}
	return out
}

// GatedLog wraps a Log and can hold After or Append calls open until
// released, which lets a test park a replay or a publish at a chosen point.
type GatedLog struct {
	streamhub.Log

	mu         sync.Mutex
	afterGate  chan struct{}
	appendGate chan struct{}
	entered    chan struct{}
	afterCalls atomic.Int64
}

// NewGatedLog wraps l with both gates open.
func NewGatedLog(l streamhub.Log) *GatedLog {
	return &GatedLog{Log: l, entered: make(chan struct{}, 1024)}
}

// HoldAfter makes every After call wait, once it starts iterating, until
// ReleaseAfter.
func (g *GatedLog) HoldAfter() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.afterGate == nil {
		g.afterGate = make(chan struct{})
	}
}

// ReleaseAfter lets held After calls proceed.
func (g *GatedLog) ReleaseAfter() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.afterGate != nil {
		close(g.afterGate)
		g.afterGate = nil
	}
}

// HoldAppend makes every Append wait until ReleaseAppend.
func (g *GatedLog) HoldAppend() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.appendGate == nil {
		g.appendGate = make(chan struct{})
	}
}

// ReleaseAppend lets held Append calls proceed.
func (g *GatedLog) ReleaseAppend() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.appendGate != nil {
		close(g.appendGate)
		g.appendGate = nil
	}
}

// AfterEntered receives one value each time an After call reaches a held gate.
func (g *GatedLog) AfterEntered() <-chan struct{} { return g.entered }

// AfterCalls reports how many times After has been called.
func (g *GatedLog) AfterCalls() int { return int(g.afterCalls.Load()) }

// Append implements streamhub.Log.
func (g *GatedLog) Append(ctx context.Context, stream string, e streamhub.Event) (streamhub.Record, error) {
	g.mu.Lock()
	gate := g.appendGate
	g.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return streamhub.Record{}, ctx.Err()
		}
	}
	return g.Log.Append(ctx, stream, e)
}

// After implements streamhub.Log.
func (g *GatedLog) After(ctx context.Context, stream string, after streamhub.Seq) iter.Seq2[streamhub.Record, error] {
	g.afterCalls.Add(1)
	return func(yield func(streamhub.Record, error) bool) {
		g.mu.Lock()
		gate := g.afterGate
		g.mu.Unlock()
		if gate != nil {
			select {
			case g.entered <- struct{}{}:
			default:
			}
			select {
			case <-gate:
			case <-ctx.Done():
				yield(streamhub.Record{}, ctx.Err())
				return
			}
		}
		for r, err := range g.Log.After(ctx, stream, after) {
			if !yield(r, err) {
				return
			}
		}
	}
}

// Forget implements streamhub.Forgetter by forwarding to the wrapped Log when
// it supports it, so wrapping a MemoryLog does not hide its cleanup.
func (g *GatedLog) Forget(ctx context.Context, stream string) error {
	if f, ok := g.Log.(streamhub.Forgetter); ok {
		return f.Forget(ctx, stream)
	}
	return nil
}
