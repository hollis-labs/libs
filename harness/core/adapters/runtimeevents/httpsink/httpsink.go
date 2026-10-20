// Package httpsink ships runtimeevents envelopes to an event hub over HTTP.
//
// It is the producer-side client for the shared event bus: an app keeps its own
// local record (database, outbox, log) and, when a hub URL is configured, also
// hands each event to a [Sink]. With no URL configured [FromEnv] returns a
// no-op sink, so an app runs unchanged without a hub.
//
// The sink never blocks or fails its caller. Write copies the event into a
// bounded queue; a background goroutine posts batches. When the queue is full
// the oldest event is dropped and counted. Network errors, 429 and 5xx
// responses are retried with backoff; other 4xx responses drop the batch and
// count it as rejected. Delivery is therefore best effort: an app that cannot
// lose events keeps its own outbox and replays it through a relay (planned,
// not part of this package yet).
//
// Wire format: POST <URL> with a JSON body {"events":[<Event>...]}, bearer
// token in the Authorization header. Any 2xx response means the whole batch was
// accepted; per-event results are not interpreted yet. The hub-side route is
// still being settled, so treat this layout as provisional.
package httpsink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

// Environment variables read by [FromEnv].
const (
	EnvURL      = "HOLLIS_EVENTS_URL"
	EnvAuth     = "HOLLIS_EVENTS_AUTH"
	EnvAuthFile = "HOLLIS_EVENTS_AUTH_FILE"
)

// Config configures a [Sink]. The zero value of every field except URL picks a
// default.
type Config struct {
	// URL is the hub ingest endpoint. Required.
	URL string
	// Token is sent as a bearer token when non-empty.
	Token string
	// Client overrides the HTTP client. Default: a client with Timeout.
	Client *http.Client
	// Timeout bounds one request. Default 5s.
	Timeout time.Duration
	// QueueSize bounds the in-memory queue. Default 1000.
	QueueSize int
	// BatchSize is the most events sent in one request. Default 100.
	BatchSize int
	// FlushInterval is how long a partial batch waits. Default 1s.
	FlushInterval time.Duration
	// MaxAttempts bounds attempts for one batch before it is dropped.
	// Default 5.
	MaxAttempts int
	// BackoffBase is the first retry delay; it doubles each attempt up to
	// 30s. Default 500ms.
	BackoffBase time.Duration
	// OnDrop, when set, is called with a count and a reason ("queue_full",
	// "rejected", "retries_exhausted", "closed") each time events are lost.
	// It must not block.
	OnDrop func(n int, reason string)
}

// Stats is a snapshot of a sink's counters.
type Stats struct {
	Sent     uint64
	Dropped  uint64
	Rejected uint64
	Retries  uint64
	Queued   int
}

// Sink is a [runtimeevents.Sink] that posts events to a hub.
type Sink struct {
	cfg    Config
	client *http.Client

	mu     sync.Mutex
	queue  []runtimeevents.Event
	closed bool
	wake   chan struct{}
	done   chan struct{}
	stop   chan struct{}

	sent, dropped, rejected, retries atomic.Uint64
}

var _ runtimeevents.Sink = (*Sink)(nil)

// New starts a Sink. It returns an error only for a missing URL.
func New(cfg Config) (*Sink, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("httpsink: URL is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1000
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 500 * time.Millisecond
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	s := &Sink{
		cfg:    cfg,
		client: client,
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
		stop:   make(chan struct{}),
	}
	go s.loop()
	return s, nil
}

// Write implements [runtimeevents.Sink]. It never blocks on the network and
// never returns an error.
func (s *Sink) Write(_ context.Context, ev runtimeevents.Event) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.drop(1, "closed")
		return nil
	}
	if len(s.queue) >= s.cfg.QueueSize {
		s.queue = s.queue[1:]
		s.drop(1, "queue_full")
	}
	s.queue = append(s.queue, ev)
	full := len(s.queue) >= s.cfg.BatchSize
	s.mu.Unlock()
	if full {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// Stats returns the current counters.
func (s *Sink) Stats() Stats {
	s.mu.Lock()
	q := len(s.queue)
	s.mu.Unlock()
	return Stats{Sent: s.sent.Load(), Dropped: s.dropped.Load(), Rejected: s.rejected.Load(), Retries: s.retries.Load(), Queued: q}
}

// Close flushes what it can until ctx ends, then stops the sender. Events
// still queued when ctx ends are dropped and counted.
func (s *Sink) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.done
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		n := len(s.queue)
		s.queue = nil
		s.mu.Unlock()
		if n > 0 {
			s.drop(n, "closed")
		}
		return ctx.Err()
	}
}

func (s *Sink) drop(n int, reason string) {
	s.dropped.Add(uint64(n))
	if s.cfg.OnDrop != nil {
		s.cfg.OnDrop(n, reason)
	}
}

func (s *Sink) take() []runtimeevents.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.queue)
	if n == 0 {
		return nil
	}
	if n > s.cfg.BatchSize {
		n = s.cfg.BatchSize
	}
	batch := append([]runtimeevents.Event(nil), s.queue[:n]...)
	s.queue = s.queue[n:]
	return batch
}

func (s *Sink) loop() {
	defer close(s.done)
	t := time.NewTicker(s.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.flushAll(true)
			return
		case <-t.C:
			s.flushAll(false)
		case <-s.wake:
			s.flushAll(false)
		}
	}
}

func (s *Sink) flushAll(closing bool) {
	for {
		batch := s.take()
		if len(batch) == 0 {
			return
		}
		s.deliver(batch, closing)
	}
}

// deliver posts one batch, retrying transient failures. While closing it makes
// a single attempt so shutdown is not held up by backoff.
func (s *Sink) deliver(batch []runtimeevents.Event, closing bool) {
	body, err := json.Marshal(struct {
		Events []runtimeevents.Event `json:"events"`
	}{batch})
	if err != nil {
		s.rejected.Add(uint64(len(batch)))
		s.drop(len(batch), "rejected")
		return
	}
	attempts := s.cfg.MaxAttempts
	if closing {
		attempts = 1
	}
	delay := s.cfg.BackoffBase
	for i := 1; ; i++ {
		retryAfter, retry := s.post(body)
		if !retry {
			if retryAfter < 0 { // rejected
				s.rejected.Add(uint64(len(batch)))
				s.drop(len(batch), "rejected")
			} else {
				s.sent.Add(uint64(len(batch)))
			}
			return
		}
		if i >= attempts {
			s.drop(len(batch), "retries_exhausted")
			return
		}
		s.retries.Add(1)
		wait := delay
		if retryAfter > wait {
			wait = retryAfter
		}
		select {
		case <-time.After(wait):
		case <-s.stop:
			// Closing: do not hold shutdown behind backoff.
			s.drop(len(batch), "closed")
			return
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

// post makes one request. It returns (0,false) on success, (-1,false) when the
// hub rejected the batch, and (retryAfter,true) for a transient failure.
func (s *Sink) post(body []byte) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return -1, false
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, true
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return 0, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		var ra time.Duration
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				ra = time.Duration(secs) * time.Second
			}
		}
		return ra, true
	default:
		return -1, false
	}
}

// Noop is the sink [FromEnv] returns when no hub is configured.
type Noop struct{}

// Write implements [runtimeevents.Sink] and discards the event.
func (Noop) Write(context.Context, runtimeevents.Event) error { return nil }

// Close implements [Closer].
func (Noop) Close(context.Context) error { return nil }

// Closer is a sink that can be flushed and stopped.
type Closer interface {
	runtimeevents.Sink
	Close(ctx context.Context) error
}

// FromEnv builds a sink from HOLLIS_EVENTS_URL and either HOLLIS_EVENTS_AUTH
// or HOLLIS_EVENTS_AUTH_FILE. With no URL it returns [Noop]. A token file that
// cannot be read is an error; an unset token is not.
func FromEnv() (Closer, error) {
	return FromConfig(Config{
		URL:   os.Getenv(EnvURL),
		Token: os.Getenv(EnvAuth),
	}, os.Getenv(EnvAuthFile))
}

// FromConfig is FromEnv for apps that carry the settings in their own config
// file. A blank cfg.URL yields [Noop]; tokenFile, when set and cfg.Token is
// empty, is read and trimmed.
func FromConfig(cfg Config, tokenFile string) (Closer, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return Noop{}, nil
	}
	if cfg.Token == "" && tokenFile != "" {
		b, err := os.ReadFile(tokenFile) //nolint:gosec // operator-supplied path
		if err != nil {
			return nil, err
		}
		cfg.Token = strings.TrimSpace(string(b))
	}
	return New(cfg)
}
