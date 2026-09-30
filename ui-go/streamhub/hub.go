package streamhub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const defaultRetainAfterClose = 60 * time.Second

type config struct {
	retention Retention
	terminal  func(Record) bool
	autoOpen  bool
	grace     time.Duration
}

// Option configures a [Hub].
type Option func(*config)

// WithRetention makes the hub call Log.Trim with r after every Publish. A
// failing Trim is reported by Publish together with the (published) record.
func WithRetention(r Retention) Option { return func(c *config) { c.retention = r } }

// WithTerminal installs the terminal predicate. When it reports true for a
// published record, that record is the stream's last: later Publish calls
// return [ErrTerminated] and subscribers get io.EOF after it. The hub does not
// know what a terminal record looks like; the predicate does.
func WithTerminal(f func(Record) bool) Option { return func(c *config) { c.terminal = f } }

// WithAutoOpen sets whether Publish and Subscribe create an unknown stream
// (default true). With false, an unknown stream is [ErrUnknownStream] until
// [Hub.Open] is called.
func WithAutoOpen(v bool) Option { return func(c *config) { c.autoOpen = v } }

// WithRetainAfterClose sets how long the hub keeps an ended stream so late
// subscribers can still replay it (default 60s, Nanite's grace). After that
// the hub forgets the stream and, if the Log implements [Forgetter], asks it
// to release the records. Zero forgets as soon as no replay is running;
// negative keeps ended streams until [Hub.Shutdown].
func WithRetainAfterClose(d time.Duration) Option { return func(c *config) { c.grace = d } }

type closeConfig struct {
	finalizer func() (Event, bool)
}

// CloseOption configures [Hub.Close].
type CloseOption func(*closeConfig)

// WithFinalizer supplies a synthesized last record for Close: if the stream
// has not ended, f is called and, when it reports true, its Event is
// published as the stream's terminal record before the stream ends.
func WithFinalizer(f func() (Event, bool)) CloseOption {
	return func(c *closeConfig) { c.finalizer = f }
}

// Hub fans records out from a [Log] to subscribers with replay, gap
// signaling and slow-consumer policies. Create one with [New]. All methods
// are safe for concurrent use.
type Hub struct {
	log Log
	cfg config
	wg  sync.WaitGroup // replay goroutines

	mu      sync.Mutex
	streams map[string]*stream
	down    bool
}

// stream is the hub's state for one stream name.
//
// Locks, outermost first: pubMu (serializes Publish/Close: Append plus
// fan-out), then mu (subscriber set and the flags below), then a
// subscription's own mu. initMu is taken alone. No Log method is ever called
// with mu held; the only Log calls under a hub lock are Append, under pubMu,
// which by contract is what orders records.
type stream struct {
	name string
	done chan struct{} // closed when the stream has been forgotten

	initMu sync.Mutex
	inited bool

	pubMu sync.Mutex

	mu            sync.Mutex
	latest        Seq // highest Seq fanned out
	ended         bool
	down          bool
	gone          bool
	subs          map[*subscription]struct{}
	replays       int // running replay goroutines
	forgetPending bool
	timer         *time.Timer
}

// New returns a Hub over l. It panics if l is nil. The hub does not own l:
// close it yourself after [Hub.Shutdown].
func New(l Log, o ...Option) *Hub {
	if l == nil {
		panic("streamhub: New requires a Log")
	}
	h := &Hub{
		log:     l,
		cfg:     config{autoOpen: true, grace: defaultRetainAfterClose},
		streams: make(map[string]*stream),
	}
	for _, f := range o {
		f(&h.cfg)
	}
	return h
}

// stream returns the initialized state for name, creating it when create is set.
func (h *Hub) stream(ctx context.Context, name string, create bool) (*stream, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: empty stream name", ErrUnknownStream)
	}
	for {
		h.mu.Lock()
		if h.down {
			h.mu.Unlock()
			return nil, ErrClosed
		}
		st := h.streams[name]
		if st == nil {
			if !create {
				h.mu.Unlock()
				return nil, fmt.Errorf("%w: %q", ErrUnknownStream, name)
			}
			st = &stream{name: name, done: make(chan struct{}), subs: make(map[*subscription]struct{})}
			h.streams[name] = st
		}
		h.mu.Unlock()

		st.mu.Lock()
		gone := st.gone
		st.mu.Unlock()
		if gone {
			select {
			case <-st.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err := h.ensureInit(ctx, st); err != nil {
			return nil, err
		}
		return st, nil
	}
}

// ensureInit loads the stream's head from the Log the first time the hub
// sees it, so a hub over a populated (reopened) Log continues its numbering
// and knows whether the stream already ended.
func (h *Hub) ensureInit(ctx context.Context, st *stream) error {
	st.initMu.Lock()
	defer st.initMu.Unlock()
	if st.inited {
		return nil
	}
	head, err := h.log.Head(ctx, st.name)
	if err != nil {
		return err
	}
	ended := head.Terminated
	if !ended && h.cfg.terminal != nil && head.Latest > head.PrunedThrough {
		for rec, err := range h.log.After(ctx, st.name, head.Latest-1) {
			if err != nil {
				return err
			}
			ended = h.cfg.terminal(rec)
		}
	}
	st.mu.Lock()
	st.latest = head.Latest
	st.ended = ended
	st.mu.Unlock()
	st.inited = true
	if ended {
		h.startGrace(st)
	}
	return nil
}

// Open makes the stream known to the hub. It is idempotent and only needed
// with WithAutoOpen(false), or to load a stream from a populated Log ahead of
// use. It returns ErrTerminated for a stream that has already ended.
func (h *Hub) Open(ctx context.Context, stream string) error {
	st, err := h.stream(ctx, stream, true)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ended {
		return ErrTerminated
	}
	return nil
}

// Head reports the log's summary of the stream, with Terminated set from the
// hub's view. The stream must be open.
func (h *Hub) Head(ctx context.Context, stream string) (Head, error) {
	st, err := h.stream(ctx, stream, false)
	if err != nil {
		return Head{}, err
	}
	head, err := h.log.Head(ctx, stream)
	if err != nil {
		return Head{}, err
	}
	st.mu.Lock()
	head.Terminated = st.ended
	st.mu.Unlock()
	return head, nil
}

// Publish appends e to the stream and fans the resulting record out. Publishes
// to one stream are serialized: Append and fan-out happen as one step, so
// subscribers see records in Seq order and a Subscribe never straddles half a
// publish. It returns [ErrTerminated] once the stream has ended.
//
// Publish does not wait for slow subscribers except under the Block policy,
// where it waits until ctx ends. When Publish returns a Record together with
// an error, the record was appended and delivered; the error reports a Block
// wait cut short by ctx, or a failed retention Trim.
func (h *Hub) Publish(ctx context.Context, stream string, e Event) (Record, error) {
	for {
		st, err := h.stream(ctx, stream, h.cfg.autoOpen)
		if err != nil {
			return Record{}, err
		}
		st.pubMu.Lock()
		rec, err := h.publishLocked(ctx, st, e, false)
		st.pubMu.Unlock()
		if errors.Is(err, errStreamGone) {
			continue
		}
		return rec, err
	}
}

var errStreamGone = errors.New("streamhub: stream forgotten")

// publishLocked runs with st.pubMu held.
func (h *Hub) publishLocked(ctx context.Context, st *stream, e Event, force bool) (Record, error) {
	st.mu.Lock()
	switch {
	case st.gone:
		st.mu.Unlock()
		return Record{}, errStreamGone
	case st.down:
		st.mu.Unlock()
		return Record{}, ErrClosed
	case st.ended:
		st.mu.Unlock()
		return Record{}, ErrTerminated
	}
	st.mu.Unlock()

	rec, err := h.log.Append(ctx, st.name, e)
	if err != nil {
		return Record{}, err
	}
	term := force || (h.cfg.terminal != nil && h.cfg.terminal(rec))

	st.mu.Lock()
	st.latest = rec.Seq
	if term {
		st.ended = true
	}
	var blocked []*subscription
	for s := range st.subs {
		b, dead := s.offer(rec, term)
		switch {
		case dead:
			delete(st.subs, s)
		case b:
			blocked = append(blocked, s)
		}
	}
	st.mu.Unlock()

	var result error
	for _, s := range blocked {
		if err := s.pushBlocking(ctx, rec, term); err != nil && result == nil {
			result = err
		}
	}
	if term {
		h.startGrace(st)
	}
	if !h.cfg.retention.isZero() {
		if err := h.log.Trim(ctx, st.name, h.cfg.retention); err != nil && result == nil {
			result = err
		}
	}
	return rec, result
}

// Close ends the stream: subscribers receive everything already published,
// then io.EOF, and Publish returns ErrTerminated. Closing an ended stream is
// a no-op. With [WithFinalizer], a synthesized terminal record is published
// first if the stream had not ended.
func (h *Hub) Close(ctx context.Context, stream string, o ...CloseOption) error {
	var cc closeConfig
	for _, f := range o {
		f(&cc)
	}
	st, err := h.stream(ctx, stream, false)
	if err != nil {
		return err
	}
	st.pubMu.Lock()
	defer st.pubMu.Unlock()

	st.mu.Lock()
	ended := st.ended
	st.mu.Unlock()
	if ended {
		return nil
	}
	if cc.finalizer != nil {
		if e, ok := cc.finalizer(); ok {
			if _, err := h.publishLocked(ctx, st, e, true); err != nil && !errors.Is(err, ErrTerminated) {
				return err
			}
			return nil
		}
	}
	st.mu.Lock()
	st.ended = true
	for s := range st.subs {
		s.markEOF()
	}
	st.mu.Unlock()
	h.startGrace(st)
	return nil
}

func (s *subscription) markEOF() {
	s.mu.Lock()
	s.eof = true
	s.signalLocked()
	s.mu.Unlock()
}

// startGrace schedules forgetting an ended stream.
func (h *Hub) startGrace(st *stream) {
	grace := h.cfg.grace
	switch {
	case grace < 0:
		return
	case grace == 0:
		h.forget(st)
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.timer == nil && !st.gone && !st.down {
		st.timer = time.AfterFunc(grace, func() { h.forget(st) })
	}
}

// forget drops an ended stream once no replay is reading it. The map entry
// stays (marked gone) until the Log has let go, so a new stream of the same
// name cannot start on top of records that are about to be removed.
func (h *Hub) forget(st *stream) {
	st.mu.Lock()
	if st.gone || st.down {
		st.mu.Unlock()
		return
	}
	if st.replays > 0 {
		st.forgetPending = true
		st.mu.Unlock()
		return
	}
	st.gone = true
	st.mu.Unlock()

	if f, ok := h.log.(Forgetter); ok {
		// Best effort: a Log that cannot forget only costs memory.
		_ = f.Forget(context.Background(), st.name)
	}
	h.mu.Lock()
	if h.streams[st.name] == st {
		delete(h.streams, st.name)
	}
	h.mu.Unlock()
	close(st.done)
}

func (st *stream) replayDone(h *Hub) {
	st.mu.Lock()
	st.replays--
	again := st.replays == 0 && st.forgetPending
	st.forgetPending = false
	st.mu.Unlock()
	if again {
		h.forget(st)
	}
}

// Subscribe registers a subscriber and starts delivering from o.After. See
// the package documentation for the algorithm: the subscriber is registered
// and the head read under the stream lock, replay runs outside it, and live
// records that arrive meanwhile are queued and de-duplicated by Seq.
//
// The subscription ends when ctx ends. Subscribe itself never waits for the
// Log.
func (h *Hub) Subscribe(ctx context.Context, stream string, o SubscribeOptions) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.Buffer <= 0 {
		o.Buffer = defaultBuffer
	}
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &subscription{
		h: h, filter: o.Filter, policy: o.Policy, buffer: o.Buffer,
		gapAsErr: o.GapAsError, cancelReplay: cancel,
	}

	var head, after Seq
	for {
		st, err := h.stream(ctx, stream, h.cfg.autoOpen)
		if err != nil {
			cancel()
			return nil, err
		}
		st.mu.Lock()
		if st.gone {
			st.mu.Unlock()
			continue
		}
		if st.down {
			st.mu.Unlock()
			cancel()
			return nil, ErrClosed
		}
		head = st.latest
		after = o.After
		if after == FromLatest {
			after = head
		}
		s.st = st
		s.mu.Lock()
		s.lastDelivered = after
		s.latestSeen = head
		s.replaying = after != head
		s.eof = st.ended
		s.mu.Unlock()
		st.subs[s] = struct{}{}
		if s.replaying {
			st.replays++
			h.wg.Add(1)
		}
		st.mu.Unlock()
		break
	}

	stop := context.AfterFunc(ctx, func() { s.closeWith(ctx.Err()) })
	s.mu.Lock()
	s.stopCtx = stop
	closed := s.closed
	s.mu.Unlock()
	if closed {
		stop()
	}
	if s.replaying {
		go s.runReplay(rctx, head, after)
	}
	return s, nil
}

// Shutdown ends every stream and subscription: subscribers drain what they
// have, then get [ErrClosed]; Publish, Subscribe and the like return
// [ErrClosed]. It waits for the hub's replay goroutines. The Log is left open.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	if h.down {
		h.mu.Unlock()
		return nil
	}
	h.down = true
	sts := make([]*stream, 0, len(h.streams))
	for _, st := range h.streams {
		sts = append(sts, st)
	}
	h.mu.Unlock()

	for _, st := range sts {
		st.mu.Lock()
		st.down = true
		if st.timer != nil {
			st.timer.Stop()
		}
		subs := make([]*subscription, 0, len(st.subs))
		for s := range st.subs {
			subs = append(subs, s)
		}
		st.mu.Unlock()
		for _, s := range subs {
			s.shutdown()
		}
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
