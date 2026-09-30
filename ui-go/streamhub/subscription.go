package streamhub

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
)

const defaultBuffer = 256

// SubscribeOptions configures one subscription.
type SubscribeOptions struct {
	// After is the cursor to resume from: records with Seq > After are
	// delivered. Zero replays from the start of retained history;
	// [FromLatest] delivers live records only.
	After Seq
	// Policy is the slow-consumer policy; the zero value is CloseAndResume.
	Policy SlowPolicy
	// Buffer bounds the live records queued for this subscriber
	// (default 256).
	Buffer int
	// Filter, when set, selects the records this subscriber sees. It runs
	// inside the hub's fan-out (and in the replay goroutine): it must be fast,
	// must not block, and must not call back into the Hub.
	Filter func(Record) bool
	// GapAsError makes a gap end the subscription: Next returns a *GapError
	// (matching ErrGap) instead of an Item with Gap set, then ErrClosed.
	GapAsError bool
}

// Item is one delivery: a record, or a gap notice. When Gap is non-nil the
// Record is zero.
type Item struct {
	Record Record
	Gap    *Gap
}

// Subscription is one consumer's view of a stream. Next is meant for one
// goroutine at a time; Close and Drops may be called from any goroutine.
type Subscription interface {
	// Next blocks for the next item. Items arrive in Seq order. It returns:
	// io.EOF once the stream has ended and everything was delivered; a
	// *SlowConsumerError (matching ErrSlowConsumer) once the buffer drained
	// after a policy closed the subscription; ErrClosed after Close or hub
	// shutdown; the subscribe context's error if that context ended; and
	// ctx.Err() if ctx ends first.
	Next(ctx context.Context) (Item, error)
	// Close ends the subscription. It is idempotent.
	Close() error
	// Drops reports how many records this subscription lost to its policy.
	Drops() uint64
}

type entry struct {
	rec Record // for a dropped-gap marker, rec.Seq is the last dropped Seq
	gap *Gap   // non-nil: a marker to render when it reaches the front
}

type subscription struct {
	h        *Hub
	st       *stream
	filter   func(Record) bool
	policy   SlowPolicy
	buffer   int
	gapAsErr bool

	mu              sync.Mutex
	q               []entry // replayed records, written only by the replay goroutine
	qN              int
	live            []entry // records that arrived through fan-out
	liveN           int
	replaying       bool
	replayedThrough Seq
	lastDelivered   Seq
	latestSeen      Seq
	eof             bool
	closed          bool
	err             error // Next result once closed
	drainErr        error // Next result once the queues drain
	drops           uint64
	consec          int
	changed         chan struct{}
	cancelReplay    context.CancelFunc
	stopCtx         func() bool
}

var _ Subscription = (*subscription)(nil)

// endedLocked reports that the subscription takes no more records.
func (s *subscription) endedLocked() bool { return s.closed || s.drainErr != nil }

func (s *subscription) waitChLocked() chan struct{} {
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}

func (s *subscription) signalLocked() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// closeLocked ends the subscription immediately, discarding queued items.
func (s *subscription) closeLocked(err error) {
	if s.closed {
		return
	}
	s.closed = true
	s.err = err
	s.q, s.live, s.qN, s.liveN = nil, nil, 0, 0
	s.cancelReplay()
	if s.stopCtx != nil {
		s.stopCtx()
	}
	s.signalLocked()
}

// resumeLocked is the cursor a resuming subscriber should pass as After: the
// highest Seq that is (or will be, after draining) in its hands with no hole
// below it. During replay the live list has a hole below it, so it is ignored.
func (s *subscription) resumeLocked() Seq {
	last := func(l []entry) (Seq, bool) {
		for i := len(l) - 1; i >= 0; i-- {
			if l[i].gap == nil {
				return l[i].rec.Seq, true
			}
		}
		return 0, false
	}
	if !s.replaying {
		if seq, ok := last(s.live); ok {
			return seq
		}
	}
	if seq, ok := last(s.q); ok {
		return seq
	}
	return s.lastDelivered
}

// drainCloseLocked ends the subscription after the queues drain.
func (s *subscription) drainCloseLocked(err error) {
	if s.endedLocked() {
		return
	}
	s.drainErr = err
	if s.replaying {
		s.live, s.liveN = nil, 0
	}
	s.cancelReplay()
	s.signalLocked()
}

func (s *subscription) slowCloseLocked() {
	s.drainCloseLocked(&SlowConsumerError{LastDelivered: s.resumeLocked()})
}

func (s *subscription) pushLiveLocked(rec Record) {
	s.live = append(s.live, entry{rec: rec})
	s.liveN++
}

func (s *subscription) noteDropLocked(seq Seq) {
	s.drops++
	s.consec++
	if n := len(s.live); n > 0 && s.live[n-1].gap != nil && s.live[n-1].gap.Reason == GapDropped {
		s.live[n-1].gap.Missed++
		s.live[n-1].rec.Seq = seq
		return
	}
	s.live = append(s.live, entry{rec: Record{Seq: seq}, gap: &Gap{Reason: GapDropped, Missed: 1}})
}

func (s *subscription) dropOldestLocked() {
	idx := slices.IndexFunc(s.live, func(e entry) bool { return e.gap == nil })
	if idx < 0 {
		return
	}
	dropped := s.live[idx].rec.Seq
	s.live = slices.Delete(s.live, idx, idx+1)
	s.liveN--
	s.drops++
	s.consec++
	if idx > 0 && s.live[idx-1].gap != nil && s.live[idx-1].gap.Reason == GapDropped {
		s.live[idx-1].gap.Missed++
		s.live[idx-1].rec.Seq = dropped
		return
	}
	s.live = slices.Insert(s.live, idx, entry{rec: Record{Seq: dropped}, gap: &Gap{Reason: GapDropped, Missed: 1}})
}

// offer is called by the publisher, holding the stream lock, for every
// record. It never blocks. blocked reports that the Block policy needs the
// publisher to wait (see pushBlocking); dead reports that the subscription no
// longer takes records and can leave the stream's set.
func (s *subscription) offer(rec Record, terminal bool) (blocked, dead bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endedLocked() {
		return false, true
	}
	s.latestSeen = rec.Seq
	if s.filter != nil && !s.filter(rec) {
		if terminal {
			s.eof = true
		}
		s.signalLocked()
		return false, false
	}
	if s.liveN >= s.buffer {
		switch s.policy.kind {
		case policyBlock:
			return true, false
		case policyDropNewest:
			s.noteDropLocked(rec.Seq)
		case policyDropOldest, policyEvictAfterN:
			s.dropOldestLocked()
			s.pushLiveLocked(rec)
			if s.policy.kind == policyEvictAfterN && s.consec >= s.policy.n {
				s.slowCloseLocked()
				return false, true
			}
		default:
			s.slowCloseLocked()
			return false, true
		}
	} else {
		s.pushLiveLocked(rec)
		s.consec = 0
	}
	if terminal {
		s.eof = true
	}
	s.signalLocked()
	return false, false
}

// pushBlocking completes a Block-policy delivery outside the stream lock. If
// ctx ends first the record is counted as dropped for this subscriber.
func (s *subscription) pushBlocking(ctx context.Context, rec Record, terminal bool) error {
	for {
		s.mu.Lock()
		if s.endedLocked() {
			s.mu.Unlock()
			return nil
		}
		if s.liveN < s.buffer {
			s.pushLiveLocked(rec)
			s.consec = 0
			if terminal {
				s.eof = true
			}
			s.signalLocked()
			s.mu.Unlock()
			return nil
		}
		ch := s.waitChLocked()
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			s.mu.Lock()
			s.noteDropLocked(rec.Seq)
			if terminal {
				s.eof = true
			}
			s.signalLocked()
			s.mu.Unlock()
			return ctx.Err()
		}
	}
}

func (s *subscription) peekSeqLocked(from []entry) (Seq, bool) {
	for _, e := range from {
		if e.gap == nil {
			return e.rec.Seq, true
		}
	}
	return 0, false
}

// popLocked removes the next deliverable item: replayed entries first, then
// (once replay is complete) live entries.
func (s *subscription) popLocked() (Item, bool) {
	for {
		fromQ := len(s.q) > 0
		var list *[]entry
		switch {
		case fromQ:
			list = &s.q
		case !s.replaying && len(s.live) > 0:
			list = &s.live
		default:
			return Item{}, false
		}
		e := (*list)[0]
		(*list)[0] = entry{}
		*list = (*list)[1:]
		if e.gap == nil {
			if fromQ {
				s.qN--
			} else {
				s.liveN--
				// Dedupe by seq: anything the replay already delivered is
				// dropped from the live list.
				if e.rec.Seq <= s.replayedThrough {
					continue
				}
			}
			s.lastDelivered = e.rec.Seq
			s.signalLocked()
			return Item{Record: e.rec}, true
		}
		g := *e.gap
		if g.Reason == GapDropped {
			g.Requested = s.lastDelivered
			g.Latest = s.latestSeen
			g.OldestAvailable = e.rec.Seq + 1
			if next, ok := s.peekSeqLocked(*list); ok {
				g.OldestAvailable = next
			} else if fromQ && !s.replaying {
				if next, ok := s.peekSeqLocked(s.live); ok {
					g.OldestAvailable = next
				}
			}
		}
		s.signalLocked()
		return Item{Gap: &g}, true
	}
}

// Next implements [Subscription].
func (s *subscription) Next(ctx context.Context) (Item, error) {
	for {
		s.mu.Lock()
		if s.closed {
			err := s.err
			s.mu.Unlock()
			return Item{}, err
		}
		if it, ok := s.popLocked(); ok {
			if it.Gap != nil && s.gapAsErr {
				ge := &GapError{Gap: *it.Gap}
				s.closeLocked(ErrClosed)
				s.mu.Unlock()
				return Item{}, ge
			}
			s.mu.Unlock()
			return it, nil
		}
		if !s.replaying {
			switch {
			case s.drainErr != nil:
				err := s.drainErr
				if errors.Is(err, ErrGap) {
					s.closeLocked(ErrClosed)
				} else {
					s.closeLocked(err)
				}
				s.mu.Unlock()
				return Item{}, err
			case s.eof:
				s.closeLocked(io.EOF)
				s.mu.Unlock()
				return Item{}, io.EOF
			}
		}
		ch := s.waitChLocked()
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return Item{}, ctx.Err()
		}
	}
}

// Close implements [Subscription].
func (s *subscription) Close() error {
	s.closeWith(ErrClosed)
	return nil
}

func (s *subscription) closeWith(err error) {
	s.mu.Lock()
	wasClosed := s.closed
	s.closeLocked(err)
	s.mu.Unlock()
	if !wasClosed {
		s.st.mu.Lock()
		delete(s.st.subs, s)
		s.st.mu.Unlock()
	}
}

// shutdown ends the subscription for a hub shutdown, unless it already
// delivered everything and is only waiting to report EOF.
func (s *subscription) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eof && !s.replaying {
		return
	}
	s.drainCloseLocked(ErrClosed)
}

// Drops implements [Subscription].
func (s *subscription) Drops() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drops
}

// enqueueRec adds a replayed record, waiting for room. It reports false when
// replay should stop (closed or ctx ended).
func (s *subscription) enqueueRec(ctx context.Context, rec Record) bool {
	for {
		s.mu.Lock()
		if s.endedLocked() {
			s.mu.Unlock()
			return false
		}
		if s.qN < s.buffer {
			s.q = append(s.q, entry{rec: rec})
			s.qN++
			s.replayedThrough = rec.Seq
			s.signalLocked()
			s.mu.Unlock()
			return true
		}
		ch := s.waitChLocked()
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}

func (s *subscription) enqueueGap(g Gap) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endedLocked() {
		return false
	}
	s.q = append(s.q, entry{gap: &g})
	s.signalLocked()
	return true
}

// runReplay is the replay goroutine: it reads the log outside every hub lock
// up to head, then hands the subscription over to live delivery.
func (s *subscription) runReplay(ctx context.Context, head, cursor Seq) {
	defer s.h.wg.Done()
	defer s.st.replayDone(s.h)
	err := s.replay(ctx, head, cursor)
	s.mu.Lock()
	if err != nil && !s.closed && s.drainErr == nil {
		s.drainErr = err
		s.live, s.liveN = nil, 0
	}
	s.replaying = false
	s.signalLocked()
	s.mu.Unlock()
}

func (s *subscription) replay(ctx context.Context, head, cursor Seq) error {
	for {
		restart := false
		for rec, err := range s.h.log.After(ctx, s.st.name, cursor) {
			if err != nil {
				gap, ok := s.asGap(ctx, err, cursor)
				if !ok {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
				if s.gapAsErr {
					return &GapError{Gap: gap}
				}
				if !s.enqueueGap(gap) {
					return nil
				}
				next := gap.OldestAvailable - 1
				if next == cursor {
					return err
				}
				cursor, restart = next, true
				break
			}
			if rec.Seq > head {
				return nil
			}
			if s.filter == nil || s.filter(rec) {
				if !s.enqueueRec(ctx, rec) {
					return nil
				}
			}
			cursor = rec.Seq
		}
		if !restart {
			return nil
		}
	}
}

// asGap turns a Log error into a Gap, asking the log for its head when the
// error does not carry one.
func (s *subscription) asGap(ctx context.Context, err error, cursor Seq) (Gap, bool) {
	var ge *GapError
	if errors.As(err, &ge) {
		return ge.Gap, true
	}
	if !errors.Is(err, ErrGap) && !errors.Is(err, ErrCursorAhead) {
		return Gap{}, false
	}
	h, herr := s.h.log.Head(ctx, s.st.name)
	if herr != nil {
		return Gap{}, false
	}
	g := Gap{Requested: cursor, OldestAvailable: h.PrunedThrough + 1, Latest: h.Latest}
	if cursor > h.Latest {
		g.Reason, g.Missed = GapCursorAhead, uint64(cursor-h.Latest)
	} else {
		g.Reason, g.Missed = GapRetention, uint64(h.PrunedThrough-min(cursor, h.PrunedThrough))
	}
	return g, true
}
