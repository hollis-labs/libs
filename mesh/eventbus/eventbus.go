package eventbus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
// as the hub should receive it; this package never parses or alters it. ID must
// be the envelope's id: the hub answers per id.
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
	// URL is the hub: either its base URL (http://host:port), to which
	// "/events" is added, or the full ingest endpoint ending in "/events".
	URL string
	// Token is sent as a bearer token. TokenFile, when set, is re-read on
	// every request (so rotation needs no restart) and wins over Token.
	Token     string
	TokenFile string
	// Client overrides the HTTP client.
	Client *http.Client

	Timeout       time.Duration // one request; default 5s
	QueueSize     int           // in-memory items; default 2000, minimum 1
	BatchSize     int           // items per request; default 100 (the hub's limit)
	MaxBatchBytes int           // body bytes per request; default 900 KiB (hub limit 1 MiB)
	FlushInterval time.Duration // default 1s
	BackoffBase   time.Duration // first retry delay, doubling; default 500ms
	BackoffMax    time.Duration // default 30s

	// MaxUnresolved is how many times the hub may answer 200 without a
	// verdict for some of an item's batch (item missing, unknown status,
	// unreadable body) before those items are dropped. Default 8.
	MaxUnresolved int

	// SpoolDir, when set, enables the disk spool: items that do not fit in
	// memory, and anything unsent at Close, are kept there and resumed by the
	// next Client on the same directory. SpoolMaxBytes bounds the unsettled
	// bytes in it (default 64 MiB); the file on disk stays within about 1.34
	// times that.
	SpoolDir      string
	SpoolMaxBytes int64

	// OnDrop is called when items are lost: reason is "queue_full",
	// "spool_full", "closed", "invalid", "unresolved", "rejected" or
	// "spool_error". OnReject is called for each item the hub rejected (not
	// retried). OnError reports transport and auth failures that are being
	// retried. None is called with an internal lock held; all must not block.
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
	SpoolBytes int64 // unsettled bytes on disk
}

// Client posts items to a hub. Create one with [New] or [FromEnv].
type Client struct {
	cfg  Config
	http *http.Client

	mu     sync.Mutex
	mem    []queued
	spool  *spool
	closed bool

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	carry   []queued // owned by the sender goroutine
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
	if cfg.MaxUnresolved <= 0 {
		cfg.MaxUnresolved = 8
	}
	cfg.URL = ingestURL(cfg.URL)
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
	if c.spool != nil && c.spool.unread() > 0 {
		c.nudge()
	}
	return c, nil
}

// ingestURL turns a hub base URL into the ingest endpoint. A URL that already
// ends in /events is used as given; a query string or fragment is kept.
func ingestURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	p := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(p, "/events") {
		p += "/events"
	}
	u.Path = p
	u.RawPath = ""
	return u.String()
}

type dropNote struct {
	n      int
	reason string
}

// emit calls OnDrop for each note. Call it without holding c.mu.
func (c *Client) emit(notes []dropNote) {
	for _, d := range notes {
		if d.n <= 0 {
			continue
		}
		c.dropped.Add(uint64(d.n))
		if c.cfg.OnDrop != nil {
			c.cfg.OnDrop(d.n, d.reason)
		}
	}
}

func (c *Client) drop(n int, reason string) { c.emit([]dropNote{{n, reason}}) }

// Publish queues an item. Do not modify it.Body afterwards.
func (c *Client) Publish(it Item) {
	if it.ID == "" || !json.Valid(it.Body) {
		c.drop(1, "invalid")
		return
	}
	var notes []dropNote
	c.mu.Lock()
	switch {
	case c.closed:
		notes = append(notes, dropNote{1, "closed"})
	case c.spool != nil && (c.spool.unread() > 0 || len(c.mem) >= c.cfg.QueueSize):
		// Everything after unsettled spool records goes behind them, so the
		// order of delivery is the order of Publish.
		n, err := c.spool.append(it)
		if err != nil {
			notes = append(notes, dropNote{1, "spool_error"})
		} else if n > 0 {
			notes = append(notes, dropNote{n, "spool_full"})
		}
	case len(c.mem) >= c.cfg.QueueSize:
		notes = append(notes, c.makeRoom(it)...)
	default:
		c.mem = append(c.mem, queued{Item: it})
	}
	full := len(c.mem) >= c.cfg.BatchSize
	c.mu.Unlock()
	c.emit(notes)
	if full {
		c.nudge()
	}
}

// makeRoom handles a full memory queue (no spool) for the incoming item:
// evict the oldest low-priority item and keep the incoming one; if everything
// held is high priority, a low-priority incoming item is the one dropped, and a
// high-priority one replaces the oldest. Caller holds c.mu.
func (c *Client) makeRoom(in Item) []dropNote {
	for i, q := range c.mem {
		if q.Priority == PriorityLow {
			c.mem = append(c.mem[:i], c.mem[i+1:]...)
			c.mem = append(c.mem, queued{Item: in})
			return []dropNote{{1, "queue_full"}}
		}
	}
	if in.Priority == PriorityLow {
		return []dropNote{{1, "queue_full"}}
	}
	c.mem = append(c.mem[1:], queued{Item: in})
	return []dropNote{{1, "queue_full"}}
}

// Stats returns the counters.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	q := len(c.mem)
	var sb int64
	if c.spool != nil {
		sb = c.spool.unread()
	}
	c.mu.Unlock()
	return Stats{Sent: c.sent.Load(), Duplicates: c.dups.Load(), Rejected: c.rejected.Load(), Dropped: c.dropped.Load(), Retries: c.retries.Load(), Queued: q, SpoolBytes: sb}
}

// Close makes one last delivery attempt, then keeps whatever is unsent in the
// spool (or drops and counts it when there is no spool). It returns when the
// sender has finished or when ctx ends, whichever is first; every caller's own
// ctx is honoured, including on repeated calls.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	first := !c.closed
	c.closed = true
	c.mu.Unlock()
	if first {
		close(c.stop)
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
func (c *Client) nextBatch() []queued {
	var notes []dropNote
	c.mu.Lock()
	if len(c.mem) == 0 && c.spool != nil && c.spool.hasUnloaded() {
		refill := c.cfg.QueueSize / 2
		if refill < 1 {
			refill = 1
		}
		items, err := c.spool.peek(refill)
		if err != nil {
			notes = append(notes, dropNote{1, "spool_error"})
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
	var batch []queued
	if n > 0 {
		batch = append([]queued(nil), c.mem[:n]...)
		c.mem = c.mem[n:]
	}
	c.mu.Unlock()
	c.emit(notes)
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
		rest, ok := c.deliver(batch, closing)
		if !ok {
			c.carry = rest
			return
		}
	}
}

// ackBatch tells the spool that the leading settled items of batch are done,
// and shifts the offsets of every item still held if the spool compacted.
func (c *Client) ackBatch(batch []queued, settled []bool) {
	var maxEnd int64
	for i := range batch {
		if !settled[i] {
			break
		}
		if batch[i].end > maxEnd {
			maxEnd = batch[i].end
		}
	}
	if maxEnd == 0 || c.spool == nil {
		return
	}
	c.mu.Lock()
	shift, err := c.spool.ackTo(maxEnd)
	if shift > 0 {
		for i := range batch {
			if batch[i].end > 0 {
				batch[i].end -= shift
			}
		}
		for i := range c.mem {
			if c.mem[i].end > 0 {
				c.mem[i].end -= shift
			}
		}
	}
	c.mu.Unlock()
	if err != nil {
		c.report(fmt.Errorf("eventbus: spool ack: %w", err))
	}
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeRetry
	outcomeTooLarge
	outcomeReject
)

type itemResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// deliver sends batch until every item has a verdict. It returns false with
// the unsettled items when sending should stop (closing, or Close called).
func (c *Client) deliver(batch []queued, closing bool) ([]queued, bool) {
	settled := make([]bool, len(batch))
	pending := make([]int, len(batch))
	for i := range pending {
		pending[i] = i
	}
	window := len(pending)
	unresolved := 0

	finishItems := func(idx []int) {
		for _, i := range idx {
			settled[i] = true
		}
		var rest []int
		for _, i := range pending {
			if !settled[i] {
				rest = append(rest, i)
			}
		}
		pending = rest
		c.ackBatch(batch, settled)
	}
	unsettled := func() []queued {
		out := make([]queued, 0, len(pending))
		for _, i := range pending {
			out = append(out, batch[i])
		}
		return out
	}

	for len(pending) > 0 {
		w := window
		if w > len(pending) {
			w = len(pending)
		}
		send := append([]int(nil), pending[:w]...)
		items := make([]Item, w)
		for k, i := range send {
			items[k] = batch[i].Item
		}
		out, wait, results := c.post(items)

		switch out {
		case outcomeReject:
			c.rejected.Add(uint64(len(send)))
			c.drop(len(send), "rejected")
			finishItems(send)
			continue
		case outcomeTooLarge:
			if w == 1 {
				c.rejected.Add(1)
				if c.cfg.OnReject != nil {
					c.cfg.OnReject(items[0].ID, "payload_too_large", "hub refused the request size")
				}
				finishItems(send)
				continue
			}
			window = w / 2
			continue
		case outcomeOK:
			var done, open []int
			for k, i := range send {
				r, ok := results[items[k].ID]
				switch {
				case ok && r.Status == "accepted":
					c.sent.Add(1)
					done = append(done, i)
				case ok && r.Status == "duplicate":
					c.dups.Add(1)
					c.sent.Add(1)
					done = append(done, i)
				case ok && r.Status == "rejected":
					c.rejected.Add(1)
					if c.cfg.OnReject != nil {
						c.cfg.OnReject(items[k].ID, r.Code, r.Message)
					}
					done = append(done, i)
				default:
					open = append(open, i)
				}
			}
			finishItems(done)
			if len(open) == 0 {
				c.backoff = c.cfg.BackoffBase
				continue
			}
			unresolved++
			if unresolved > c.cfg.MaxUnresolved {
				c.drop(len(open), "unresolved")
				finishItems(open)
				continue
			}
			c.report(fmt.Errorf("eventbus: hub gave no verdict for %d of %d events", len(open), len(send)))
			wait = 0
		}

		// Transient failure, or a 200 that left items unresolved: back off.
		if closing {
			return unsettled(), false
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
			return unsettled(), false
		}
	}
	return nil, true
}

func (c *Client) token() string {
	if c.cfg.TokenFile != "" {
		if b, err := os.ReadFile(c.cfg.TokenFile); err == nil {
			return strings.TrimSpace(string(b))
		} else {
			c.report(fmt.Errorf("eventbus: read token file: %w", err))
		}
	}
	return c.cfg.Token
}

func (c *Client) report(err error) {
	if c.cfg.OnError != nil {
		c.cfg.OnError(err)
	}
}

// post makes one request. For a 200 it returns the per-id verdicts the hub
// gave (empty if the body could not be read).
func (c *Client) post(items []Item) (outcome, time.Duration, map[string]itemResult) {
	var buf bytes.Buffer
	buf.WriteString(`{"events":[`)
	for i, it := range items {
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
		c.report(err)
		return outcomeReject, 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := c.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.report(err)
		return outcomeRetry, 0, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	switch {
	case resp.StatusCode == http.StatusOK:
		return outcomeOK, 0, parseResults(body)
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return outcomeTooLarge, 0, nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// A rotated or not-yet-issued credential: keep the events and retry.
		c.report(fmt.Errorf("eventbus: hub returned %d", resp.StatusCode))
		return outcomeRetry, 0, nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode >= 500:
		var ra time.Duration
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			ra = time.Duration(secs) * time.Second
		}
		c.report(fmt.Errorf("eventbus: hub returned %d", resp.StatusCode))
		return outcomeRetry, ra, nil
	default:
		c.report(fmt.Errorf("eventbus: hub returned %d, batch rejected", resp.StatusCode))
		return outcomeReject, 0, nil
	}
}

// parseResults reads {"results":[{id,status,...}]}, or a bare array of the same
// objects. Anything else yields no verdicts.
func parseResults(body []byte) map[string]itemResult {
	var list []itemResult
	var wrapped struct {
		Results []itemResult `json:"results"`
	}
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Results != nil {
		list = wrapped.Results
	} else if json.Unmarshal(body, &list) != nil {
		return nil
	}
	m := make(map[string]itemResult, len(list))
	for _, r := range list {
		if r.ID != "" {
			m[r.ID] = r
		}
	}
	return m
}

// finish runs after Close: one delivery attempt per batch, then the leftovers
// go to the spool or are dropped.
func (c *Client) finish() {
	c.pump(true)
	c.mu.Lock()
	left := append(append([]queued(nil), c.carry...), c.mem...)
	c.carry, c.mem = nil, nil
	var notes []dropNote
	switch {
	case len(left) == 0:
	case c.spool == nil:
		notes = append(notes, dropNote{len(left), "closed"})
	default:
		// Items that came from the spool are still there; only the ones that
		// never reached it need writing, in front of the unsettled records.
		var fresh []Item
		for _, q := range left {
			if q.end == 0 {
				fresh = append(fresh, q.Item)
			}
		}
		if len(fresh) > 0 {
			n, err := c.spool.prepend(fresh)
			switch {
			case err != nil:
				notes = append(notes, dropNote{len(fresh), "spool_error"})
			case n > 0:
				notes = append(notes, dropNote{n, "spool_full"})
			}
		}
	}
	c.mu.Unlock()
	c.emit(notes)
}

// Noop is the publisher used when no hub is configured.
type Noop struct{}

// Publish discards the item.
func (Noop) Publish(Item) {}

// Close does nothing.
func (Noop) Close(context.Context) error { return nil }
