package tether

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

type EnvironmentStreamOptions struct {
	// Empty SessionID subscribes to the whole environment. A nil AfterSeq
	// starts from a snapshot; &0 explicitly requests replay from the beginning.
	SessionID string
	AfterSeq  *int64
}

// EnvironmentState separates route transport health from projection freshness.
// Transport: connecting, connected, disconnected, offline, blocked.
// Freshness: unknown, catching_up, fresh, stale, gap. Only synchronized marks
// fresh; a reachable daemon alone does not mean the caller has current data.
type EnvironmentState struct {
	Transport  string
	Freshness  string
	RouteIndex int
	AfterSeq   int64
}

// EnvironmentUpdate is delivered in order with backpressure. Exactly one of
// Event/Snapshot/Gap is populated, or all are nil for a state change. Error is
// sanitized connection information, not a raw HTTP response or credential.
type EnvironmentUpdate struct {
	State    EnvironmentState
	Event    *EnvironmentEvent
	Snapshot *EnvironmentSnapshot
	Gap      *EnvironmentGap
	Error    error
}

// EnvironmentSubscription owns one supervised read stream. Errors carries the
// terminal outcome (including context cancellation), then closes. Mutations
// must be invoked separately on a connection's Client and are never replayed.
type EnvironmentSubscription struct {
	Updates <-chan EnvironmentUpdate
	Errors  <-chan error
	Done    <-chan struct{}
	cancel  context.CancelFunc
	wake    chan struct{}
	mu      sync.Mutex
	online  bool
}

func (s *EnvironmentSubscription) Close() { s.cancel() }

// Wake signals credential rotation or a network change. An authentication
// failure waits for this signal rather than consuming timed retry attempts.
func (s *EnvironmentSubscription) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *EnvironmentSubscription) SetOnline(online bool) {
	s.mu.Lock()
	s.online = online
	s.mu.Unlock()
	s.Wake()
}
func (s *EnvironmentSubscription) isOnline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online
}

func (e *EnvironmentClient) Subscribe(ctx context.Context, opts EnvironmentStreamOptions) (*EnvironmentSubscription, error) {
	if opts.AfterSeq != nil && *opts.AfterSeq < 0 {
		return nil, errors.New("tether: after_seq must be nonnegative")
	}
	// Copy cursor ownership away from a caller that may later change its value.
	after := int64(0)
	if opts.AfterSeq != nil {
		after = *opts.AfterSeq
	}
	ctx, cancel := context.WithCancel(ctx)
	updates := make(chan EnvironmentUpdate)
	errs := make(chan error, 1)
	done := make(chan struct{})
	s := &EnvironmentSubscription{Updates: updates, Errors: errs, Done: done, cancel: cancel, wake: make(chan struct{}, 1), online: true}
	go func() {
		defer close(done)
		defer close(errs)
		defer close(updates)
		defer cancel()
		errs <- e.supervise(ctx, s, opts.SessionID, after, opts.AfterSeq == nil, updates)
	}()
	return s, nil
}

var errEnvironmentWake = errors.New("tether: environment wakeup")
var errEnvironmentGap = errors.New("tether: environment snapshot required")

func environmentBlocked(err error) bool {
	var auth *EnvironmentAuthenticationError
	var identity *EnvironmentIdentityError
	var protocol *ProtocolMismatchError
	return errors.As(err, &auth) || errors.As(err, &identity) || errors.As(err, &protocol)
}

func environmentTerminal(err error) bool {
	var protocol *ProtocolMismatchError
	var identity *EnvironmentIdentityError
	return errors.As(err, &protocol) || errors.As(err, &identity)
}

func (e *EnvironmentClient) retryDelay(attempt int) time.Duration {
	if attempt > 9 {
		attempt = 9
	}
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Second * time.Duration(1<<attempt)
	jitter := e.opts.Jitter()
	if math.IsNaN(jitter) || math.IsInf(jitter, 0) {
		jitter = 1
	}
	if jitter < 0.5 {
		jitter = 0.5
	}
	if jitter > 1.5 {
		jitter = 1.5
	}
	delay = time.Duration(float64(delay) * jitter)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay
}

func (e *EnvironmentClient) supervise(ctx context.Context, s *EnvironmentSubscription, sessionID string, cursor int64, snapshotRequired bool, updates chan<- EnvironmentUpdate) error {
	state := EnvironmentState{Transport: "connecting", Freshness: "unknown", RouteIndex: -1, AfterSeq: cursor}
	emit := func(update EnvironmentUpdate) error {
		update.State = state
		select {
		case updates <- update:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	waitWake := func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.wake:
			return nil
		}
	}
	cooldown := make(map[int]time.Time)
	attempt := 0
	var next *EnvironmentConnection
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !s.isOnline() {
			state.Transport = "offline"
			if state.Freshness != "unknown" && state.Freshness != "gap" {
				state.Freshness = "stale"
			}
			if err := emit(EnvironmentUpdate{}); err != nil {
				return err
			}
			if err := waitWake(); err != nil {
				return err
			}
			continue
		}
		state.Transport = "connecting"
		if state.Freshness == "fresh" {
			state.Freshness = "catching_up"
		}
		if err := emit(EnvironmentUpdate{}); err != nil {
			return err
		}
		conn := next
		next = nil
		var err error
		if conn == nil {
			conn, err = e.connectInterruptible(ctx, s, cooldown)
		}
		if errors.Is(err, errEnvironmentWake) {
			clear(cooldown)
			continue
		}
		var connectedAt time.Time
		if err == nil {
			state.RouteIndex = conn.RouteIndex
			state.Freshness = "catching_up"
			if snapshotRequired {
				snapshotCtx, cancel := context.WithTimeout(ctx, e.opts.ConnectTimeout)
				var snapshot EnvironmentSnapshot
				snapshot, err = conn.Snapshot(snapshotCtx, sessionID)
				cancel()
				if err == nil {
					cursor = snapshot.HighWaterSeq
					state.AfterSeq = cursor
					err = emit(EnvironmentUpdate{Snapshot: &snapshot})
					if err == nil {
						snapshotRequired = false
					}
				}
			}
			if err == nil {
				next, connectedAt, err = e.followConnection(ctx, s, conn, sessionID, &cursor, &state, cooldown, emit)
				state.AfterSeq = cursor
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errEnvironmentWake) {
			continue
		}
		if errors.Is(err, errEnvironmentGap) {
			snapshotRequired = true
			next = conn
			continue
		}
		if next != nil {
			continue
		}
		state.Transport = "disconnected"
		if state.Freshness != "unknown" && state.Freshness != "gap" {
			state.Freshness = "stale"
		}
		if environmentBlocked(err) {
			state.Transport = "blocked"
		}
		if emitErr := emit(EnvironmentUpdate{Error: err}); emitErr != nil {
			return emitErr
		}
		if environmentTerminal(err) {
			return err
		}
		if environmentBlocked(err) {
			if err := waitWake(); err != nil {
				return err
			}
			clear(cooldown)
			continue
		}
		if !connectedAt.IsZero() && e.opts.Clock.Now().Sub(connectedAt) >= e.opts.StableAfter {
			attempt = 0
		}
		timer := e.opts.Clock.NewTimer(e.retryDelay(attempt))
		attempt++
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-s.wake:
			clear(cooldown)
		case <-timer.C():
		}
		timer.Stop()
	}
}

func (e *EnvironmentClient) connectInterruptible(ctx context.Context, s *EnvironmentSubscription, cooldown map[int]time.Time) (*EnvironmentConnection, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn *EnvironmentConnection
		err  error
	}
	done := make(chan result, 1)
	go func() { conn, err := e.connect(ctx, cooldown); done <- result{conn, err} }()
	select {
	case result := <-done:
		return result.conn, result.err
	case <-ctx.Done():
		cancel()
		<-done
		return nil, ctx.Err()
	case <-s.wake:
		cancel()
		<-done
		return nil, errEnvironmentWake
	}
}

func (e *EnvironmentClient) betterRoute(ctx context.Context, conn *EnvironmentConnection, cooldown map[int]time.Time) (*EnvironmentConnection, error) {
	for i := 0; i < conn.RouteIndex; i++ {
		if until, ok := cooldown[i]; ok && e.opts.Clock.Now().Before(until) {
			continue
		}
		descriptor, err := e.descriptor(ctx, i, e.opts.ProbeTimeout)
		var mismatch *ProtocolMismatchError
		if errors.As(err, &mismatch) {
			return nil, err
		}
		var identity *EnvironmentIdentityError
		if errors.As(err, &identity) {
			cooldown[i] = e.opts.Clock.Now().Add(e.opts.RouteCooldown)
		}
		if err != nil {
			continue
		}
		better, err := e.authenticate(ctx, i, descriptor)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			return better, nil
		}
		if environmentTerminal(err) {
			return nil, err
		}
		cooldown[i] = e.opts.Clock.Now().Add(e.opts.RouteCooldown)
	}
	return nil, nil
}

func (e *EnvironmentClient) preflightInterruptible(ctx context.Context, s *EnvironmentSubscription, conn *EnvironmentConnection, cooldown map[int]time.Time) (*EnvironmentConnection, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn *EnvironmentConnection
		err  error
	}
	done := make(chan result, 1)
	go func() { conn, err := e.betterRoute(ctx, conn, cooldown); done <- result{conn, err} }()
	select {
	case result := <-done:
		return result.conn, result.err
	case <-ctx.Done():
		cancel()
		<-done
		return nil, ctx.Err()
	case <-s.wake:
		cancel()
		<-done
		if s.isOnline() {
			return nil, nil
		}
		return nil, errEnvironmentWake
	}
}

func (e *EnvironmentClient) followConnection(ctx context.Context, s *EnvironmentSubscription, conn *EnvironmentConnection, sessionID string, cursor *int64, state *EnvironmentState, cooldown map[int]time.Time, emit func(EnvironmentUpdate) error) (*EnvironmentConnection, time.Time, error) {
	readCtx, cancel := context.WithCancel(ctx)
	frames := make(chan environmentFrame)
	done := make(chan error, 1)
	go func() {
		done <- e.readEnvironmentStream(readCtx, conn, sessionID, *cursor, func(frame environmentFrame) error {
			select {
			case frames <- frame:
				return nil
			case <-readCtx.Done():
				return readCtx.Err()
			}
		})
	}()
	// Every exit closes the HTTP body and joins its reader before reconnecting.
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	var connectedAt time.Time
	var preflight EnvironmentTimer
	var tick <-chan time.Time
	if conn.RouteIndex > 0 {
		preflight = e.opts.Clock.NewTimer(e.opts.PreflightInterval)
		tick = preflight.C()
	}
	defer func() {
		if preflight != nil {
			preflight.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil, connectedAt, ctx.Err()
		case err := <-done:
			finished = true
			// A promoted route that fails stream establishment is cooled down;
			// ordinary established stream drops retain normal resume/backoff.
			if connectedAt.IsZero() {
				cooldown[conn.RouteIndex] = e.opts.Clock.Now().Add(e.opts.RouteCooldown)
			}
			return nil, connectedAt, err
		case frame := <-frames:
			if frame.ready {
				connectedAt = e.opts.Clock.Now()
				state.Transport = "connected"
				if err := emit(EnvironmentUpdate{}); err != nil {
					return nil, connectedAt, err
				}
			} else if frame.event != nil {
				state.AfterSeq = frame.event.Seq
				if err := emit(EnvironmentUpdate{Event: frame.event}); err != nil {
					return nil, connectedAt, err
				}
				*cursor = frame.event.Seq
				state.AfterSeq = *cursor
			} else if frame.gap != nil {
				state.Freshness = "gap"
				if err := emit(EnvironmentUpdate{Gap: frame.gap}); err != nil {
					return nil, connectedAt, err
				}
				return nil, connectedAt, errEnvironmentGap
			} else if frame.synchronized != nil {
				*cursor = *frame.synchronized
				state.AfterSeq = *cursor
				state.Freshness = "fresh"
				if err := emit(EnvironmentUpdate{}); err != nil {
					return nil, connectedAt, err
				}
			}
		case <-tick:
			better, err := e.preflightInterruptible(ctx, s, conn, cooldown)
			if err != nil || better != nil {
				return better, connectedAt, err
			}
			preflight.Stop()
			preflight = e.opts.Clock.NewTimer(e.opts.PreflightInterval)
			tick = preflight.C()
		case <-s.wake:
			if !s.isOnline() {
				return nil, connectedAt, errEnvironmentWake
			}
			better, err := e.preflightInterruptible(ctx, s, conn, cooldown)
			if err != nil || better != nil {
				return better, connectedAt, err
			}
		}
	}
}
