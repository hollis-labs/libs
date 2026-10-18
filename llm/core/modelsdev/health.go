package modelsdev

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RefreshError is safe to expose: it contains no URL, body, header or local path.
// Code identifies the failure class; HTTPStatus is set only for HTTP failures.
type RefreshError struct {
	Code       string
	HTTPStatus int
}

func (e *RefreshError) Error() string {
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("modelsdev: %s (%d)", e.Code, e.HTTPStatus)
	}
	return "modelsdev: " + e.Code
}

// RefreshHealth is a point-in-time view of one source's refresh lifecycle.
// Attempts count Refresh calls that acquired the gate, not individual HTTP retries.
// LastSuccess is the timestamp of the published snapshot (including disk reloads).
type RefreshHealth struct {
	Running             bool      `json:"running"`
	SourceID            string    `json:"source_id"`
	State               string    `json:"state"`
	Refreshing          bool      `json:"refreshing"`
	Attempts            uint64    `json:"attempts"`
	ConsecutiveFailures uint64    `json:"consecutive_failures"`
	LastAttempt         time.Time `json:"last_attempt,omitzero"`
	LastFinished        time.Time `json:"last_finished,omitzero"`
	LastSuccess         time.Time `json:"last_success,omitzero"`
	NextAttempt         time.Time `json:"next_attempt,omitzero"`
	LastError           string    `json:"last_error,omitempty"`
	HTTPStatus          int       `json:"http_status,omitempty"`
	LastCacheError      string    `json:"last_cache_error,omitempty"`
	LegacyCache         bool      `json:"legacy_cache,omitempty"`
}

// WithRefreshTimeout bounds gate wait and fetching/retries, but not user callbacks.
// Nonpositive durations use the default (30s). Custom transports must honor context.
func WithRefreshTimeout(d time.Duration) Option { return func(c *Client) { c.refreshTimeout = d } }

// WithMaxResponseBytes bounds decoded HTTP bodies and cache files. Nonpositive
// values or values above 1 GiB use 64 MiB. Oversized responses never replace a good catalog.
func WithMaxResponseBytes(n int64) Option { return func(c *Client) { c.maxResponseBytes = n } }

// WithRetryBackoff configures HTTP retry and background failure delays. The
// background delay doubles from min to max. Invalid minima use 1s; max is at least min.
func WithRetryBackoff(min, max time.Duration) Option {
	return func(c *Client) { c.retryMin, c.retryMax = min, max }
}

// WithLegacyCacheSource explicitly binds an old catalog.json to a source URL.
// Only use it when the application knows which source wrote that unbound cache.
// Without it legacy files are ignored; source changes must never reuse another catalog.
func WithLegacyCacheSource(url string) Option { return func(c *Client) { c.legacySource = url } }

// Health reports freshness independently of refresh errors: fresh data can
// coexist with a failed manual refresh. No source URL or raw error is exposed.
func (c *Client) Health() RefreshHealth {
	c.ensureCatalog()
	c.mu.RLock()
	defer c.mu.RUnlock()
	h := c.health
	h.SourceID, h.LastSuccess, h.LegacyCache = c.sourceID(), c.lastFetched, c.legacyCache
	h.State = "empty"
	if c.catalog != nil {
		h.State = "fresh"
		if c.legacyCache || time.Since(c.lastFetched) >= c.ttl {
			h.State = "stale"
		}
	}
	if h.NextAttempt.IsZero() && c.catalog != nil && !c.legacyCache {
		h.NextAttempt = c.lastFetched.Add(c.ttl)
	}
	return h
}

func (c *Client) beginAttempt() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.Attempts++
	c.health.LastAttempt = time.Now()
	c.health.Refreshing = true
}

func (c *Client) finishAttempt(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := &c.health
	h.Refreshing, h.LastFinished = false, time.Now()
	h.LastError, h.HTTPStatus = "", 0
	if err == nil {
		h.ConsecutiveFailures = 0
		h.NextAttempt = c.lastFetched.Add(c.ttl)
		return
	}
	h.ConsecutiveFailures++
	h.LastError = "refresh"
	var failure *RefreshError
	switch {
	case errors.Is(err, context.Canceled):
		h.LastError = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		h.LastError = "timeout"
	case errors.As(err, &failure):
		h.LastError, h.HTTPStatus = failure.Code, failure.HTTPStatus
	}
	delay := c.retryMin
	for n := uint64(1); n < h.ConsecutiveFailures && delay < c.retryMax; n++ {
		if delay > c.retryMax/2 {
			delay = c.retryMax
			break
		}
		delay *= 2
	}
	h.NextAttempt = h.LastFinished.Add(delay)
}
