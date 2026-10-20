package eventbus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Priority decides which items a full queue or spool gives up first.
type Priority uint8

const (
	// PriorityLow is for activity and debug events: dropped first.
	PriorityLow Priority = iota
	// PriorityHigh is for state, approval, audit and alert events: dropped
	// only when nothing else is left.
	PriorityHigh
)

// Item is one event ready to send. Body is the complete JSON envelope exactly
// as the hub should receive it; this package never parses or alters it.
type Item struct {
	ID       string
	Priority Priority
	Body     json.RawMessage
}

// Publisher is what an app holds. Publish never blocks on the network and
// never fails the caller.
type Publisher interface {
	Publish(Item)
	Close(ctx context.Context) error
}

// Config configures a [Client]. Only URL is required.
type Config struct {
	// URL is the hub ingest endpoint (POST /events).
	URL string
	// Token is sent as a bearer token. TokenFile, when set, is re-read on
	// every request (so rotation needs no restart) and wins over Token.
	Token     string
	TokenFile string
	// Client overrides the HTTP client.
	Client *http.Client

	Timeout       time.Duration // one request; default 5s
	QueueSize     int           // in-memory items; default 2000
	BatchSize     int           // items per request; default 100 (the hub's limit)
	MaxBatchBytes int           // body bytes per request; default 900 KiB (hub limit 1 MiB)
	FlushInterval time.Duration // default 1s
	BackoffBase   time.Duration // first retry delay, doubling; default 500ms
	BackoffMax    time.Duration // default 30s

	// SpoolDir, when set, enables the disk spool: items that do not fit in
	// memory, and anything unsent at Close, are kept there and resumed by the
	// next Client on the same directory. SpoolMaxBytes bounds it (default
	// 64 MiB).
	SpoolDir      string
	SpoolMaxBytes int64

	// OnDrop is called when items are lost: reason is "queue_full",
	// "spool_full", "closed", "invalid" or "spool_error". OnReject is called
	// for each item the hub rejected (not retried). OnError reports transport
	// and auth failures that are being retried. All must not block.
	OnDrop   func(n int, reason string)
	OnReject func(id, code, message string)
	OnError  func(err error)
}

// Stats is a snapshot of the counters.
type Stats struct {
	Sent       uint64 // accepted or duplicate
	Duplicates uint64
	Rejected   uint64
	Dropped    uint64
	Retries    uint64
	Queued     int
	SpoolBytes int64
}

// Client posts items to a hub. Create one with [New] or [FromEnv].
type Client struct {
	cfg  Config
	http *http.Client

	mu     sync.Mutex
	mem    []Item
	spool  *spool
	closed bool

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	carry   []Item // owned by the sender goroutine
	backoff time.Duration

	sent, dups, rejected, dropped, retries atomic.Uint64
}

var _ Publisher = (*Client)(nil)

// New starts a Client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("eventbus: URL is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 2000
	}
	if cfg.BatchSize <= 0 || cfg.BatchSize > 100 {
		cfg.BatchSize = 100
	}
	if cfg.MaxBatchBytes <= 0 {
		cfg.MaxBatchBytes = 900 << 10
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 500 * time.Millisecond
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 30 * time.Second
	}
	c := &Client{
		cfg:     cfg,
		http:    cfg.Client,
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		backoff: cfg.BackoffBase,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: cfg.Timeout}
	}
	if cfg.SpoolDir != "" {
		max := cfg.SpoolMaxBytes
		if max <= 0 {
			max = 64 << 20
		}
		sp, err := openSpool(cfg.SpoolDir, max)
		if err != nil {
			return nil, err
		}
		c.spool = sp
	}
	go c.run()
	if c.spool != nil && !c.spool.empty() {
		c.nudge()
	}
	return c, nil
}

// Publish queues an item. It copies nothing: do not modify it.Body afterwards.
func (c *Client) Publish(it Item) {
	if !json.Valid(it.Body) {
		c.drop(1, "invalid")
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.drop(1, "closed")
		return
	}
	var lost int
	var reason string
	switch {
	case c.spool != nil && (!c.spool.empty() || len(c.mem) >= c.cfg.QueueSize):
		n, err := c.spool.append(it)
		if err != nil {
			lost, reason = 1, "spool_error"
		} else if n > 0 {
			lost, reason = n, "spool_full"
		}
	case len(c.mem) >= c.cfg.QueueSize:
		c.evictOne()
		c.mem = append(c.mem, it)
		lost, reason = 1, "queue_full"
	default:
		c.mem = append(c.mem, it)
	}
	full := len(c.mem) >= c.cfg.BatchSize
	c.mu.Unlock()
	if lost > 0 {
		c.drop(lost, reason)
	}
	if full {
		c.nudge()
	}
}

// evictOne removes the oldest low-priority item, or the oldest item if none is
// low priority. Caller holds c.mu.
func (c *Client) evictOne() {
	for i, it := range c.mem {
		if it.Priority == PriorityLow {
			c.mem = append(c.mem[:i], c.mem[i+1:]...)
			return
		}
	}
	c.mem = c.mem[1:]
}

// Stats returns the counters.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	q := len(c.mem)
	var sb int64
	if c.spool != nil {
		sb = c.spool.pending()
	}
	c.mu.Unlock()
	return Stats{Sent: c.sent.Load(), Duplicates: c.dups.Load(), Rejected: c.rejected.Load(), Dropped: c.dropped.Load(), Retries: c.retries.Load(), Queued: q, SpoolBytes: sb}
}

// Close makes one last delivery attempt, then keeps whatever is unsent in the
// spool (or drops and counts it when there is no spool). It stops waiting when
// ctx ends.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.done
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	close(c.stop)
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) drop(n int, reason string) {
	c.dropped.Add(uint64(n))
	if c.cfg.OnDrop != nil {
		c.cfg.OnDrop(n, reason)
	}
}

func (c *Client) nudge() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) run() {
	defer close(c.done)
	t := time.NewTicker(c.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			c.finish()
			return
		case <-t.C:
		case <-c.wake:
		}
		c.pump(false)
	}
}

// nextBatch returns the next items to send, refilling memory from the spool
// when memory is empty so order is kept.
func (c *Client) nextBatch() []Item {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.mem) == 0 && c.spool != nil && !c.spool.empty() {
		items, err := c.spool.takeFront(c.cfg.QueueSize / 2)
		if err != nil {
			c.drop(1, "spool_error")
		}
		c.mem = append(c.mem, items...)
	}
	n, size := 0, 0
	for n < len(c.mem) && n < c.cfg.BatchSize {
		l := len(c.mem[n].Body) + 1
		if n > 0 && size+l > c.cfg.MaxBatchBytes {
			break
		}
		size += l
		n++
	}
	if n == 0 {
		return nil
	}
	batch := append([]Item(nil), c.mem[:n]...)
	c.mem = c.mem[n:]
	return batch
}

// pump sends until nothing is left or, when closing, the first failure.
func (c *Client) pump(closing bool) {
	for {
		batch := c.carry
		c.carry = nil
		if len(batch) == 0 {
			batch = c.nextBatch()
		}
		if len(batch) == 0 {
			return
		}
		if !c.deliver(batch, closing) {
			return
		}
	}
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeRetry
	outcomeTooLarge
	outcomeReject
)

// deliver sends one batch. It returns false when the batch was not delivered
// and sending should stop (closing or stop requested); the batch is then in
// c.carry.
func (c *Client) deliver(batch []Item, closing bool) bool {
	for {
		out, wait := c.post(batch)
		switch out {
		case outcomeOK:
			c.backoff = c.cfg.BackoffBase
			return true
		case outcomeReject:
			c.rejected.Add(uint64(len(batch)))
			c.drop(len(batch), "rejected")
			return true
		case outcomeTooLarge:
			if len(batch) == 1 {
				c.rejected.Add(1)
				if c.cfg.OnReject != nil {
					c.cfg.OnReject(batch[0].ID, "payload_too_large", "hub refused the request size")
				}
				return true
			}
			mid := len(batch) / 2
			if !c.deliver(batch[:mid], closing) {
				c.carry = append(append([]Item(nil), c.carry...), batch[mid:]...)
				return false
			}
			batch = batch[mid:]
			continue
		}
		// outcomeRetry
		if closing {
			c.carry = batch
			return false
		}
		c.retries.Add(1)
		d := c.backoff
		if wait > d {
			d = wait
		}
		if c.backoff < c.cfg.BackoffMax {
			c.backoff *= 2
		}
		select {
		case <-time.After(d):
		case <-c.stop:
			c.carry = batch
			return false
		}
	}
}

type itemResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) token() string {
	if c.cfg.TokenFile != "" {
		if b, err := os.ReadFile(c.cfg.TokenFile); err == nil {
			return strings.TrimSpace(string(b))
		} else if c.cfg.OnError != nil {
			c.cfg.OnError(fmt.Errorf("eventbus: read token file: %w", err))
		}
	}
	return c.cfg.Token
}

// post makes one request for batch and settles the counters for a 200.
func (c *Client) post(batch []Item) (outcome, time.Duration) {
	var buf bytes.Buffer
	buf.WriteString(`{"events":[`)
	for i, it := range batch {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(it.Body)
	}
	buf.WriteString(`]}`)

	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, &buf)
	if err != nil {
		return outcomeReject, 0
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := c.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.report(err)
		return outcomeRetry, 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	switch {
	case resp.StatusCode == http.StatusOK:
		c.settle(batch, body)
		return outcomeOK, 0
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return outcomeTooLarge, 0
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// A rotated or not-yet-issued credential: keep the events and retry.
		c.report(fmt.Errorf("eventbus: hub returned %d", resp.StatusCode))
		return outcomeRetry, 0
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode >= 500:
		var ra time.Duration
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			ra = time.Duration(secs) * time.Second
		}
		c.report(fmt.Errorf("eventbus: hub returned %d", resp.StatusCode))
		return outcomeRetry, ra
	default:
		c.report(fmt.Errorf("eventbus: hub returned %d, batch rejected", resp.StatusCode))
		return outcomeReject, 0
	}
}

func (c *Client) report(err error) {
	if c.cfg.OnError != nil {
		c.cfg.OnError(err)
	}
}

// settle reads the per-item results of a 200. The hub answers
// {"results":[{id,status,code,message,cursor}...]}; a bare array is accepted
// too, and an unreadable body counts every item as accepted.
func (c *Client) settle(batch []Item, body []byte) {
	var wrapped struct {
		Results []itemResult `json:"results"`
	}
	results := wrapped.Results
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Results != nil {
		results = wrapped.Results
	} else {
		var bare []itemResult
		if json.Unmarshal(body, &bare) == nil {
			results = bare
		}
	}
	byID := make(map[string]itemResult, len(results))
	for _, r := range results {
		byID[r.ID] = r
	}
	for _, it := range batch {
		r, ok := byID[it.ID]
		switch {
		case ok && r.Status == "rejected":
			c.rejected.Add(1)
			if c.cfg.OnReject != nil {
				c.cfg.OnReject(it.ID, r.Code, r.Message)
			}
		case ok && r.Status == "duplicate":
			c.dups.Add(1)
			c.sent.Add(1)
		default:
			c.sent.Add(1)
		}
	}
}

// finish runs after Close: one delivery attempt per batch, then the leftovers
// go to the spool or are dropped.
func (c *Client) finish() {
	c.pump(true)
	c.mu.Lock()
	left := append(append([]Item(nil), c.carry...), c.mem...)
	c.carry, c.mem = nil, nil
	sp := c.spool
	c.mu.Unlock()
	if len(left) == 0 {
		return
	}
	if sp == nil {
		c.drop(len(left), "closed")
		return
	}
	c.mu.Lock()
	n, err := sp.prepend(left)
	c.mu.Unlock()
	switch {
	case err != nil:
		c.drop(len(left), "spool_error")
	case n > 0:
		c.drop(n, "spool_full")
	}
}

// Noop is the publisher used when no hub is configured.
type Noop struct{}

// Publish discards the item.
func (Noop) Publish(Item) {}

// Close does nothing.
func (Noop) Close(context.Context) error { return nil }
