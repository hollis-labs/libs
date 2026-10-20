package eventbus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Priority decides what a full queue or spool gives up first: debug, then
// activity, and protected items only as the last resort.
type Priority uint8

const (
	// PriorityDebug is for debug events: dropped first.
	PriorityDebug Priority = iota
	// PriorityActivity is for activity events: dropped after debug.
	PriorityActivity
	// PriorityHigh is for state, approval, audit and alert events. They are
	// protected: lost only when nothing else can go, and then recorded (see
	// [Loss]).
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
	// every request (so rotation needs no restart) and wins over Token. With
	// neither, no Authorization header is sent at all (a local relay that holds
	// the credential needs none).
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

	// MaxUnresolved is how many times the hub may answer 200 without a valid
	// verdict for the same events (missing, unknown status, unreadable body)
	// before those events are quarantined, so one bad event cannot hold up
	// the queue behind it. Default 8; negative means retry for as long as the
	// client runs.
	MaxUnresolved int

	// A 401, or a 403 other than app_mismatch, is treated as transient by
	// default: a service often starts before its credential exists, and a
	// credential is renewed or granted at runtime. The batch is kept and
	// retried with capped backoff until the failures have lasted RetryAuthMax
	// (default 10 minutes; negative means no ceiling), then it is quarantined,
	// reported and counted, never dropped. StrictAuth makes them final
	// immediately instead. A 403 app_mismatch is always final.
	RetryAuthMax time.Duration
	StrictAuth   bool

	// SpoolDir, when set, enables the disk spool, the quarantine file and the
	// loss record: items that do not fit in memory, and anything unsent at
	// Close, are kept there and resumed by the next Client on the same
	// directory. SpoolMaxBytes bounds the unsettled bytes in the spool (default
	// 64 MiB; the file stays within about 1.34 times that). QuarantineMaxBytes
	// bounds quarantine.jsonl (default 8 MiB, oldest records go first).
	SpoolDir           string
	SpoolMaxBytes      int64
	QuarantineMaxBytes int64

	// GapMarker, when set, builds the event that announces a persisted [Loss]
	// to the hub. The client calls it, and publishes what it returns, the next
	// time the hub accepts events, then clears the record. This package does
	// not know the envelope, so the caller (the harness adapter) builds it.
	GapMarker func(Loss) (Item, error)

	// Logger receives warnings (protected-item loss, quarantine). Default
	// slog.Default().
	Logger *slog.Logger

	// OnDrop is called when items are lost: reason is "queue_full",
	// "spool_full", "high_priority_dropped" (a protected item lost to a full
	// queue or spool, the last resort), "closed", "invalid" or "spool_error".
	// OnReject is called for each item the hub rejected. OnQuarantine is called
	// for each item moved to quarantine; reason is "rejected", "bad_request",
	// "auth", "payload_too_large", "app_mismatch", "unresolved" or "http_<code>".
	// OnError reports transport and auth failures and quarantines. None is
	// called with an internal lock held; all must not block.
	OnDrop       func(n int, reason string)
	OnReject     func(id, code, message string)
	OnQuarantine func(it Item, reason, code string)
	OnError      func(err error)
}

// Stats is a snapshot of the counters.
type Stats struct {
	Sent       uint64 // accepted or duplicate
	Duplicates uint64
	Rejected   uint64 // rejected by the hub (also quarantined)
	// Quarantined counts events set aside undelivered; none was sent.
	Quarantined uint64
	Dropped     uint64 // lost to a full queue or spool, or discarded
	// DroppedHigh counts the protected (high-priority) items among Dropped.
	DroppedHigh     uint64
	Retries         uint64
	Queued          int
	SpoolBytes      int64 // unsettled bytes on disk
	QuarantineBytes int64
	Losses          []Loss // persisted protected-loss records not yet announced
}

// Client posts items to a hub. Create one with [New] or [FromEnv].
type Client struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger

	mu     sync.Mutex
	mem    []queued
	spool  *spool
	quar   *quarantine
	loss   *lossBook
	closed bool
	errs   []error // reported by emit, outside the lock

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	carry   []queued // owned by the sender goroutine
	backoff time.Duration

	sent, dups, rejected, quarantined, dropped, droppedHigh, retries atomic.Uint64
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
	if cfg.MaxUnresolved == 0 {
		cfg.MaxUnresolved = 8
	}
	if cfg.RetryAuthMax == 0 {
		cfg.RetryAuthMax = 10 * time.Minute
	}
	if cfg.QuarantineMaxBytes <= 0 {
		cfg.QuarantineMaxBytes = 8 << 20
	}
	cfg.URL = ingestURL(cfg.URL)
	c := &Client{
		cfg:     cfg,
		http:    cfg.Client,
		log:     cfg.Logger,
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		backoff: cfg.BackoffBase,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: cfg.Timeout}
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	c.quar = openQuarantine(cfg.SpoolDir, cfg.QuarantineMaxBytes)
	c.loss = openLossBook(cfg.SpoolDir)
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
	if (c.spool != nil && c.spool.unread() > 0) || len(c.loss.recs) > 0 {
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

// emit counts the notes, calls OnDrop for each and reports the errors collected
// under the lock. Call it without holding c.mu.
func (c *Client) emit(notes []dropNote) {
	c.mu.Lock()
	errs := c.errs
	c.errs = nil
	c.mu.Unlock()
	for _, err := range errs {
		c.report(err)
	}
	for _, d := range notes {
		if d.n <= 0 {
			continue
		}
		c.dropped.Add(uint64(d.n))
		if d.reason == "high_priority_dropped" {
			c.droppedHigh.Add(uint64(d.n))
			c.log.Warn("eventbus: protected events dropped: the queue or spool is full and nothing less important was left", "count", d.n)
		}
		if c.cfg.OnDrop != nil {
			c.cfg.OnDrop(d.n, d.reason)
		}
	}
}

func (c *Client) drop(n int, reason string) { c.emit([]dropNote{{n, reason}}) }

// recordLoss adds n protected items to the persisted loss record. Caller holds
// c.mu.
func (c *Client) recordLoss(n int) {
	if n <= 0 {
		return
	}
	if err := c.loss.add(lossClass, n, time.Now().UTC()); err != nil {
		c.errs = append(c.errs, fmt.Errorf("eventbus: save loss record: %w", err))
	}
}

// lossNotes turns spool loss counts into notes and records the protected ones.
// Caller holds c.mu.
func (c *Client) lossNotes(l lossCounts) []dropNote {
	c.recordLoss(l[PriorityHigh])
	return []dropNote{
		{l[PriorityDebug] + l[PriorityActivity], "spool_full"},
		{l[PriorityHigh], "high_priority_dropped"},
	}
}

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
		lost, err := c.spool.append(it)
		if err != nil {
			notes = append(notes, dropNote{1, "spool_error"})
		} else {
			notes = append(notes, c.lossNotes(lost)...)
		}
	case len(c.mem) >= c.cfg.QueueSize:
		notes = c.makeRoom(it)
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

// makeRoom handles a full memory queue (no spool) for the incoming item. The
// oldest item of the least important class held goes; if the incoming item is
// less important than everything held, it is the one refused; a protected
// incoming item replaces the oldest protected one only when nothing else is
// left, and that loss is recorded. Caller holds c.mu.
func (c *Client) makeRoom(in Item) []dropNote {
	idx, lowest := -1, PriorityHigh+1
	for i, q := range c.mem {
		if q.Priority < lowest {
			idx, lowest = i, q.Priority
		}
	}
	if in.Priority < lowest {
		return []dropNote{{1, "queue_full"}}
	}
	c.mem = append(c.mem[:idx], c.mem[idx+1:]...)
	c.mem = append(c.mem, queued{Item: in})
	if lowest == PriorityHigh {
		c.recordLoss(1)
		return []dropNote{{1, "high_priority_dropped"}}
	}
	return []dropNote{{1, "queue_full"}}
}

// Stats returns the counters.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	st := Stats{Queued: len(c.mem), Losses: c.loss.list(), QuarantineBytes: c.quar.size}
	if c.spool != nil {
		st.SpoolBytes = c.spool.unread()
	}
	c.mu.Unlock()
	st.Sent, st.Duplicates, st.Rejected = c.sent.Load(), c.dups.Load(), c.rejected.Load()
	st.Quarantined, st.Dropped, st.DroppedHigh, st.Retries = c.quarantined.Load(), c.dropped.Load(), c.droppedHigh.Load(), c.retries.Load()
	return st
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
			if !closing {
				c.announceLoss()
			}
			return
		}
		rest, ok := c.deliver(batch, closing)
		if !ok {
			c.carry = rest
			return
		}
	}
}

// announceLoss publishes a gap marker for each persisted loss record that has
// none in flight, once the queue is empty (so the marker follows everything
// already pending). The marker is its own reachability probe: if the hub is
// down it waits in the queue like any event. The record stays on disk, tied to
// the marker's id, until the hub accepts that marker; only then are the
// announced items taken off it (see ackMarker).
func (c *Client) announceLoss() {
	if c.cfg.GapMarker == nil {
		return
	}
	c.mu.Lock()
	var snaps []Loss
	for _, r := range c.loss.list() {
		if r.MarkerID == "" {
			snaps = append(snaps, r)
		}
	}
	c.mu.Unlock()
	for _, snap := range snaps {
		it, err := c.cfg.GapMarker(snap)
		if err != nil {
			c.report(fmt.Errorf("eventbus: build gap marker: %w", err))
			continue
		}
		c.mu.Lock()
		err = c.loss.beginMarker(snap.Class, it.ID, snap)
		c.mu.Unlock()
		if err != nil {
			c.report(fmt.Errorf("eventbus: save loss record: %w", err))
			continue
		}
		c.Publish(it)
	}
}

// markerAccepted is called for each event the hub accepted.
func (c *Client) markerAccepted(id string) {
	c.mu.Lock()
	_, err := c.loss.ackMarker(id)
	c.mu.Unlock()
	if err != nil {
		c.report(fmt.Errorf("eventbus: save loss record: %w", err))
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

// quarantineItems sets items aside undelivered, whole or not at all, and
// reports whether they are safely recorded. When it returns false nothing was
// written and the caller must keep the items (and retry): they are not settled
// and not counted as quarantined. They are never counted as sent.
func (c *Client) quarantineItems(items []Item, reason, code, message string) bool {
	now := time.Now().UTC()
	recs := make([]quarantineRec, len(items))
	for i, it := range items {
		recs[i] = quarantineRec{At: now, Reason: reason, Code: code, Message: message, ID: it.ID, Prio: it.Priority, Body: it.Body}
	}
	c.mu.Lock()
	err, trimErr := c.quar.addAll(recs)
	c.mu.Unlock()
	if err != nil {
		c.report(fmt.Errorf("eventbus: cannot write quarantine, keeping %d event(s) to retry: %w", len(items), err))
		return false
	}
	if trimErr != nil {
		c.report(fmt.Errorf("eventbus: trim quarantine: %w", trimErr))
	}
	c.quarantined.Add(uint64(len(items)))
	for _, it := range items {
		if c.cfg.OnQuarantine != nil {
			c.cfg.OnQuarantine(it, reason, code)
		}
	}
	c.log.Warn("eventbus: events quarantined undelivered", "count", len(items), "reason", reason, "code", code)
	c.report(fmt.Errorf("eventbus: %d event(s) quarantined (reason %s %s); none was sent", len(items), reason, code))
	return true
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeRetry
	outcomeTooLarge
	outcomeFinal // the request failed for good: quarantine its events
	outcomeAuth  // 401 or 403: transient until it has lasted RetryAuthMax
)

type itemResult struct {
	ID            string
	Status        string
	Code          string
	Message       string
	Cursor        string // the cursor, if it was a non-empty JSON string
	CursorPresent bool   // some cursor was sent, of any type
}

// postResult is what one request came to.
type postResult struct {
	out     outcome
	wait    time.Duration
	reason  string // for outcomeFinal
	code    string
	results []*itemResult // for outcomeOK, one per sent item, nil when no valid verdict
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
	var authSince time.Time // when the current run of auth failures began

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
	itemsOf := func(idx []int) []Item {
		out := make([]Item, len(idx))
		for k, i := range idx {
			out[k] = batch[i].Item
		}
		return out
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
		items := itemsOf(send)
		res := c.post(items)
		wait := res.wait
		if res.out != outcomeAuth {
			authSince = time.Time{}
		}

		switch res.out {
		case outcomeFinal:
			if c.quarantineItems(items, res.reason, res.code, "") {
				finishItems(send)
				continue
			}
			// The quarantine could not be written: keep the events and retry.
		case outcomeAuth:
			if authSince.IsZero() {
				authSince = time.Now()
			}
			if c.cfg.StrictAuth || (c.cfg.RetryAuthMax > 0 && time.Since(authSince) >= c.cfg.RetryAuthMax) {
				if c.quarantineItems(items, "auth", res.code, "") {
					authSince = time.Time{}
					finishItems(send)
					continue
				}
			}
			c.report(fmt.Errorf("eventbus: hub returned an auth failure (%s); keeping events, retrying for up to %s", res.code, c.cfg.RetryAuthMax))
			// fall through to the backoff below
		case outcomeTooLarge:
			if w == 1 {
				if c.quarantineItems(items, "payload_too_large", "", "hub refused the request size") {
					finishItems(send)
					continue
				}
				// Could not record it: keep the event and retry below.
			} else {
				window = w / 2
				continue
			}
		case outcomeOK:
			var done, open []int
			for k, i := range send {
				r := res.results[k]
				switch {
				case r != nil && r.Status == "accepted":
					c.sent.Add(1)
					c.markerAccepted(items[k].ID)
					done = append(done, i)
				case r != nil && r.Status == "duplicate":
					c.dups.Add(1)
					c.sent.Add(1)
					c.markerAccepted(items[k].ID)
					done = append(done, i)
				case r != nil && r.Status == "rejected":
					if c.quarantineItems(items[k:k+1], "rejected", r.Code, r.Message) {
						c.rejected.Add(1)
						if c.cfg.OnReject != nil {
							c.cfg.OnReject(items[k].ID, r.Code, r.Message)
						}
						done = append(done, i)
					} else {
						open = append(open, i)
					}
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
			if c.cfg.MaxUnresolved > 0 && unresolved > c.cfg.MaxUnresolved {
				if c.quarantineItems(itemsOf(open), "unresolved", "", "no valid verdict from the hub") {
					finishItems(open)
					continue
				}
			}
			c.report(fmt.Errorf("eventbus: hub gave no valid verdict for %d of %d events", len(open), len(send)))
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

// post makes one request.
func (c *Client) post(items []Item) postResult {
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
		return postResult{out: outcomeFinal, reason: "bad_request"}
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := c.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.report(err)
		return postResult{out: outcomeRetry}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	st := resp.StatusCode

	switch {
	case st == http.StatusOK:
		return postResult{out: outcomeOK, results: verdicts(items, body)}
	case st == http.StatusRequestEntityTooLarge:
		return postResult{out: outcomeTooLarge}
	case st == http.StatusForbidden && errorCode(body) == "app_mismatch":
		// The credential is bound to another app than the events name: no
		// retry can succeed.
		return postResult{out: outcomeFinal, reason: "app_mismatch", code: "app_mismatch"}
	case st == http.StatusUnauthorized || st == http.StatusForbidden:
		code := errorCode(body)
		if code == "" {
			code = strconv.Itoa(st)
		}
		return postResult{out: outcomeAuth, code: code}
	case st == http.StatusNotFound || st == http.StatusMethodNotAllowed:
		// The hub is not accepting events yet (ingest off, or no events store).
		c.report(fmt.Errorf("eventbus: hub returned %d: ingest not enabled yet; keeping events", st))
		return postResult{out: outcomeRetry}
	case st == http.StatusTooManyRequests || st == http.StatusServiceUnavailable || st >= 500:
		var ra time.Duration
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			ra = time.Duration(secs) * time.Second
		}
		c.report(fmt.Errorf("eventbus: hub returned %d", st))
		return postResult{out: outcomeRetry, wait: ra}
	case st == http.StatusBadRequest:
		return postResult{out: outcomeFinal, reason: "bad_request", code: errorCode(body)}
	default:
		return postResult{out: outcomeFinal, reason: "http_" + strconv.Itoa(st), code: errorCode(body)}
	}
}

// errorCode reads the code of a {"error":{"code":"..."}} body, or "".
func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Error.Code
}

// rejectCodes are the codes the hub gives a rejected event.
var rejectCodes = map[string]bool{
	"unknown_kind": true, "schema_invalid": true, "forbidden_kind": true,
	"payload_too_large": true, "id_conflict": true,
}

// verdicts turns a 200 body into one entry per request item: the item's
// verdict, or nil when it has none. Per the contract the answer is
// {"results":[...]} with exactly one result per item, in order, each carrying
// the item's id. accepted and duplicate need a cursor that is a non-empty JSON
// string and no code; rejected needs one of the reject codes and no cursor at
// all. Anything else is no verdict: a body that is not that object (a bare
// array included), a result for another id, an unknown status, a cursor of
// another type. If the number of results differs from the number of items,
// results are matched by id instead, and an item whose id occurs more than once
// in the batch gets no verdict.
func verdicts(items []Item, body []byte) []*itemResult {
	out := make([]*itemResult, len(items))
	var wrapped struct {
		Results []struct {
			ID      string          `json:"id"`
			Status  string          `json:"status"`
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Cursor  json.RawMessage `json:"cursor"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &wrapped) != nil || wrapped.Results == nil {
		return out
	}
	list := make([]itemResult, len(wrapped.Results))
	for i, r := range wrapped.Results {
		ir := itemResult{ID: r.ID, Status: r.Status, Code: r.Code, Message: r.Message}
		raw := bytes.TrimSpace(r.Cursor)
		ir.CursorPresent = len(raw) > 0 && string(raw) != "null"
		if ir.CursorPresent {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				ir.Cursor = s
			}
		}
		list[i] = ir
	}
	valid := func(r *itemResult) bool {
		switch r.Status {
		case "accepted", "duplicate":
			return r.Cursor != "" && r.Code == ""
		case "rejected":
			return rejectCodes[r.Code] && !r.CursorPresent
		}
		return false
	}
	if len(list) == len(items) {
		for k := range items {
			if list[k].ID == items[k].ID && valid(&list[k]) {
				out[k] = &list[k]
			}
		}
		return out
	}
	occurs := make(map[string]int, len(items))
	for _, it := range items {
		occurs[it.ID]++
	}
	byID := make(map[string]*itemResult, len(list))
	seen := make(map[string]int, len(list))
	for i := range list {
		seen[list[i].ID]++
		byID[list[i].ID] = &list[i]
	}
	for k, it := range items {
		if occurs[it.ID] > 1 || seen[it.ID] != 1 {
			continue
		}
		if r := byID[it.ID]; valid(r) {
			out[k] = r
		}
	}
	return out
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
		var high int
		for _, q := range left {
			if q.Priority == PriorityHigh {
				high++
			}
		}
		c.recordLoss(high)
		notes = append(notes, dropNote{len(left) - high, "closed"}, dropNote{high, "high_priority_dropped"})
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
			lost, err := c.spool.prepend(fresh)
			if err != nil {
				notes = append(notes, dropNote{len(fresh), "spool_error"})
			} else {
				notes = append(notes, c.lossNotes(lost)...)
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
