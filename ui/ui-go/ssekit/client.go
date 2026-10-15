package ssekit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"mime"
	"net/http"
	"time"
)

// Client reads event streams over HTTP and reconnects with the last event id.
type Client struct {
	hc             *http.Client
	defaults       []StreamOption
	maxServerRetry time.Duration
}

// DefaultMaxServerRetry is the default cap on a server-sent retry: value.
const DefaultMaxServerRetry = 5 * time.Minute

// ClientOption configures NewClient.
type ClientOption func(*Client)

// WithDefaults sets stream options applied to every Stream call; options given
// to Stream itself come after and win.
func WithDefaults(o ...StreamOption) ClientOption {
	return func(c *Client) { c.defaults = append(c.defaults, o...) }
}

// WithMaxServerRetry caps the reconnect delay a server can set with retry:. A
// server that sends a larger value (or a buggy or hostile one sending an absurd
// value) would otherwise park the client for that long. d <= 0 keeps the
// default, DefaultMaxServerRetry (5 minutes).
func WithMaxServerRetry(d time.Duration) ClientOption {
	return func(c *Client) {
		if d > 0 {
			c.maxServerRetry = d
		}
	}
}

// NewClient builds a Client on a copy of hc (http.DefaultClient when nil). The
// copy's Timeout is zeroed: a client-wide timeout would cut every long-lived
// stream, so use the idle timeout and the request context instead.
func NewClient(hc *http.Client, o ...ClientOption) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	cp := *hc
	cp.Timeout = 0
	c := &Client{hc: &cp, maxServerRetry: DefaultMaxServerRetry}
	for _, f := range o {
		f(c)
	}
	return c
}

// Reconnect describes one scheduled reconnect, for WithOnReconnect.
type Reconnect struct {
	// Attempt counts consecutive reconnects without an event in between, from 1.
	Attempt int
	// Err is why the connection ended: io.EOF for a clean close, ErrIdle,
	// *StatusError for a retryable status, or a transport error.
	Err error
	// Delay is how long the client waits before reconnecting.
	Delay time.Duration
	// LastEventID is the id it will resume from ("" if none).
	LastEventID string
}

// StreamOption configures Stream (or, through WithDefaults, every Stream).
type StreamOption func(*streamConfig)

type streamConfig struct {
	backoff       []time.Duration
	jitter        float64
	idle          time.Duration
	maxEvent      int
	isTerminal    func(Event) bool
	isFinal       func(int) bool
	continuity    func(prev, next string) bool
	maxReconnects int
	onReconnect   func(Reconnect)
}

func defaultStreamConfig() streamConfig {
	return streamConfig{
		backoff:  []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second},
		jitter:   0.2,
		idle:     45 * time.Second,
		maxEvent: DefaultMaxEventBytes,
		isFinal: func(code int) bool {
			return code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
		},
		maxReconnects: -1,
	}
}

// WithBackoff sets the reconnect delays: attempt n waits schedule[n], the last
// entry repeating. jitter (0 to 1) spreads each delay by up to that fraction
// either way. Default: 1, 2, 4, 8, 16 seconds with jitter 0.2. A server-sent
// retry: overrides the schedule.
func WithBackoff(schedule []time.Duration, jitter float64) StreamOption {
	return func(c *streamConfig) {
		if len(schedule) > 0 {
			c.backoff = append([]time.Duration(nil), schedule...)
		}
		c.jitter = max(0, min(jitter, 1))
	}
}

// WithIdleTimeout reconnects when no bytes at all (comments included) arrive
// for d. Default 45 seconds, three default heartbeats; d <= 0 disables it.
func WithIdleTimeout(d time.Duration) StreamOption {
	return func(c *streamConfig) { c.idle = d }
}

// WithStreamMaxEventBytes bounds one event block; see WithMaxEventBytes.
func WithStreamMaxEventBytes(n int) StreamOption {
	return func(c *streamConfig) {
		if n > 0 {
			c.maxEvent = n
		}
	}
}

// WithIsTerminal marks events after which the stream is over: the event is
// delivered and the client does not reconnect.
func WithIsTerminal(f func(Event) bool) StreamOption {
	return func(c *streamConfig) { c.isTerminal = f }
}

// WithIsFinalStatus decides which non-200 statuses end the stream instead of
// being retried. Default: every 4xx except 408 and 429.
func WithIsFinalStatus(f func(int) bool) StreamOption {
	return func(c *streamConfig) { c.isFinal = f }
}

// WithContinuity checks each change of event id: f(prev, next) reports whether
// next may follow prev. If not, the stream yields a *GapError and stops. It is
// consulted only when the id changes and a previous id exists; what "follows"
// means, and whether a replayed overlap is acceptable, is the application's call.
func WithContinuity(f func(prev, next string) bool) StreamOption {
	return func(c *streamConfig) { c.continuity = f }
}

// WithMaxReconnects gives up after n consecutive reconnects that received no
// event; the error yielded wraps the last cause. 0 never reconnects; a
// negative n (the default) retries forever.
func WithMaxReconnects(n int) StreamOption {
	return func(c *streamConfig) { c.maxReconnects = n }
}

// WithOnReconnect observes every scheduled reconnect (logging, metrics).
func WithOnReconnect(f func(Reconnect)) StreamOption {
	return func(c *streamConfig) { c.onReconnect = f }
}

// Stream connects and yields events, reconnecting after a drop, an idle
// timeout or a retryable status until a terminal event, a final status, a
// continuity failure, ctx ending or the caller stopping the iteration.
//
// newReq builds each request; it is given the last event id ("" on the first
// connect) so applications that resume by query parameter can put it in the
// URL. The client also sends it as Last-Event-ID, sets Accept, and applies ctx.
// Event.ID on yielded events is the running last event id (see Event). Errors
// are yielded as (Event{}, err) and end the sequence; transient failures are
// retried silently (see WithOnReconnect). A 204 response ends the stream
// without error, as the specification requires.
func (c *Client) Stream(ctx context.Context, newReq func(lastEventID string) (*http.Request, error), o ...StreamOption) iter.Seq2[Event, error] {
	cfg := defaultStreamConfig()
	for _, f := range c.defaults {
		f(&cfg)
	}
	for _, f := range o {
		f(&cfg)
	}
	return func(yield func(Event, error) bool) {
		s := &stream{c: c, cfg: cfg, newReq: newReq, yield: yield}
		s.run(ctx)
	}
}

type stream struct {
	c      *Client
	cfg    streamConfig
	newReq func(string) (*http.Request, error)
	yield  func(Event, error) bool

	lastID     string
	serverWait time.Duration
	progressed bool
}

func (s *stream) run(ctx context.Context) {
	attempt := 0
	for {
		fin, cause := s.once(ctx)
		if fin {
			return
		}
		if s.progressed {
			attempt = 0
			s.progressed = false
		}
		if s.cfg.maxReconnects >= 0 && attempt >= s.cfg.maxReconnects {
			s.yield(Event{}, fmt.Errorf("ssekit: giving up after %d reconnect attempts: %w", attempt, cause))
			return
		}
		d := s.delay(attempt)
		attempt++
		if s.cfg.onReconnect != nil {
			s.cfg.onReconnect(Reconnect{Attempt: attempt, Err: cause, Delay: d, LastEventID: s.lastID})
		}
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			s.yield(Event{}, ctx.Err())
			return
		}
	}
}

func (s *stream) delay(attempt int) time.Duration {
	if s.serverWait > 0 {
		return min(s.serverWait, s.c.maxServerRetry)
	}
	d := s.cfg.backoff[min(attempt, len(s.cfg.backoff)-1)]
	if s.cfg.jitter > 0 {
		d += time.Duration((rand.Float64()*2 - 1) * s.cfg.jitter * float64(d)) //nolint:gosec // jitter, not security
	}
	return max(d, 0)
}

// once makes one connection. fin reports that the sequence is over (the final
// outcome, if any, has already been yielded); otherwise cause says why the
// connection ended and a reconnect follows.
func (s *stream) once(ctx context.Context) (fin bool, cause error) {
	req, err := s.newReq(s.lastID)
	if err != nil {
		s.yield(Event{}, err)
		return true, nil
	}
	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	req = req.WithContext(actx)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	if req.Header.Get("Cache-Control") == "" {
		req.Header.Set("Cache-Control", "no-cache")
	}
	if s.lastID != "" {
		req.Header.Set("Last-Event-ID", s.lastID)
	}

	resp, err := s.c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			s.yield(Event{}, ctx.Err())
			return true, nil
		}
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		return true, nil
	case resp.StatusCode != http.StatusOK:
		se := &StatusError{Code: resp.StatusCode, Status: resp.Status}
		if s.cfg.isFinal(resp.StatusCode) {
			s.yield(Event{}, se)
			return true, nil
		}
		return false, se
	}
	if mt, _, perr := mime.ParseMediaType(resp.Header.Get("Content-Type")); perr != nil || mt != "text/event-stream" {
		s.yield(Event{}, ErrNotEventStream)
		return true, nil
	}

	var body io.Reader = resp.Body
	var idle *time.Timer
	if s.cfg.idle > 0 {
		idle = time.AfterFunc(s.cfg.idle, func() { cancel(ErrIdle) })
		defer idle.Stop()
		body = &activityReader{r: resp.Body, touch: func() { idle.Reset(s.cfg.idle) }}
	}
	p := newParser(body, s.cfg.maxEvent)
	for {
		ev, err := p.next()
		// A retry: value rides the next event; one that arrived just before
		// the stream broke is still pending in the parser.
		if d := max(ev.Retry, p.takeRetry()); d > 0 {
			s.serverWait = d
		}
		if err != nil {
			switch {
			case ctx.Err() != nil:
				s.yield(Event{}, ctx.Err())
				return true, nil
			case errors.Is(context.Cause(actx), ErrIdle):
				return false, ErrIdle
			case errors.Is(err, ErrEventTooLarge):
				s.yield(Event{}, err)
				return true, nil
			}
			return false, err
		}
		s.progressed = true
		if s.cfg.continuity != nil && s.lastID != "" && ev.ID != s.lastID && !s.cfg.continuity(s.lastID, ev.ID) {
			s.yield(Event{}, &GapError{Prev: s.lastID, Next: ev.ID, Event: ev})
			return true, nil
		}
		s.lastID = ev.ID
		// The watchdog measures the server's silence, not the consumer's speed.
		if idle != nil {
			idle.Stop()
		}
		if !s.yield(ev, nil) {
			return true, nil
		}
		if s.cfg.isTerminal != nil && s.cfg.isTerminal(ev) {
			return true, nil
		}
		if idle != nil {
			idle.Reset(s.cfg.idle)
		}
	}
}

// activityReader reports every read that returned bytes.
type activityReader struct {
	r     io.Reader
	touch func()
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.touch()
	}
	return n, err
}
