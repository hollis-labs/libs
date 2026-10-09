package scheduler

import (
	"context"
	"crypto/rand"
	"math/big"
	"sync"
	"time"
)

const (
	// DefaultConcurrency bounds simultaneous Runner.Enqueue calls.
	DefaultConcurrency = 4
	// DefaultFireTimeout is the cooperative deadline for one Enqueue call.
	DefaultFireTimeout = 30 * time.Second
)

// WithConcurrency sets the worker bound. Nonpositive values retain the default.
func WithConcurrency(n int) Option {
	return func(e *Engine) {
		if n > 0 {
			e.concurrency = n
		}
	}
}

// WithFireTimeout sets the deadline passed to Runner.Enqueue. The effective
// claim lease is at least this timeout plus one second. Runners must honor context.
func WithFireTimeout(timeout time.Duration) Option {
	return func(e *Engine) {
		if timeout > 0 {
			e.fireTimeout = timeout
		}
	}
}

// WithRandomSource supplies injectable dispatch/retry jitter. Calls are serialized.
func WithRandomSource(source RandomSource) Option {
	return func(e *Engine) { e.random = source }
}

type globalRandom struct{}

func (globalRandom) Int63n(n int64) int64 {
	value, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0
	}
	return value.Int64()
}

func (e *Engine) jitter(maxDelay time.Duration) time.Duration {
	e.randomMu.Lock()
	defer e.randomMu.Unlock()
	return DrawJitter(maxDelay, e.random)
}

// submitFire reserves bounded capacity before creating a goroutine. Full pools
// leave durable fires pending for a later tick, rather than growing a goroutine queue.
func (e *Engine) submitFire(ctx context.Context, fire Fire, admitted *sync.WaitGroup) {
	if admitted != nil {
		select {
		case e.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
	} else {
		select {
		case e.slots <- struct{}{}:
		default:
			return
		}
	}
	e.workMu.Lock()
	if e.stopping || e.active[fire.ID] {
		<-e.slots
		e.workMu.Unlock()
		return
	}
	e.active[fire.ID] = true
	e.workers.Add(1)
	if admitted != nil {
		admitted.Add(1)
	}
	e.workMu.Unlock()
	go func() {
		defer func() {
			e.workMu.Lock()
			delete(e.active, fire.ID)
			<-e.slots
			e.workMu.Unlock()
			e.workers.Done()
			if admitted != nil {
				admitted.Done()
			}
		}()
		e.dispatchFire(ctx, fire, e.clock.Now().UTC())
	}()
}
