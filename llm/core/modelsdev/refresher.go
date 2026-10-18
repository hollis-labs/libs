package modelsdev

import (
	"context"
	"time"
)

// StartRefresher runs Run in a goroutine. Use Run directly when shutdown must
// wait for an in-flight refresh and its callback to finish.
func (c *Client) StartRefresher(ctx context.Context) { go c.Run(ctx) }

// Run refreshes stale/missing data and retries failures with bounded backoff.
// It returns after cancellation and after its own refresh and callback finish.
// Callbacks may read the client but must not recursively call Refresh or Run.
func (c *Client) Run(ctx context.Context) {
	// Only one background loop owns scheduling for this client.
	select {
	case c.runGate <- struct{}{}:
		defer func() { <-c.runGate }()
	case <-ctx.Done():
		return
	}
	c.mu.Lock()
	c.health.Running = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.health.Running = false; c.mu.Unlock() }()
	c.ensureCatalog()
	for {
		if ctx.Err() != nil {
			return
		}
		wait := c.nextRefreshIn()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return
		}
		// A manual refresh may have changed the schedule while the timer waited.
		if c.nextRefreshIn() > 0 {
			continue
		}
		_ = c.Refresh(ctx) // Failure details and the next retry are retained in Health.
	}
}

func (c *Client) nextRefreshIn() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	target := c.health.NextAttempt
	if target.IsZero() {
		if c.catalog == nil || c.legacyCache {
			return 0
		}
		target = c.lastFetched.Add(c.ttl)
	}
	if d := time.Until(target); d > 0 {
		return d
	}
	return 0
}
