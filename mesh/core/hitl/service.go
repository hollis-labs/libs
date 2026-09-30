package hitl

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ExpiryPolicyRef is the policy_ref Service writes on an expired outcome.
const ExpiryPolicyRef = "request.expires_at"

const maxAttempts = 16

// Options configures a Service. Every field is optional.
type Options struct {
	// Clock returns the current time (default time.Now). Every decision a
	// Service takes about one operation uses a single reading of it.
	Clock func() time.Time
	// NewID returns a fresh item id (default: "item_" plus 16 random bytes in
	// hex). Resolution ids are derived the same way.
	NewID func() string
	// PollInterval bounds how long Await sleeps between looks at the store,
	// which is what lets Await see changes made through another Service or
	// process (default 250ms). In-process changes wake waiters at once.
	PollInterval time.Duration
	// ValidateResponse adds a profile check to Respond (for example: approval
	// decisions are approved|denied). Core only requires kind and decision to
	// be non-empty and kind to equal the request kind.
	ValidateResponse func(requestKind string, r Response) error
}

// Service is the reference implementation of the lifecycle over a Store:
// enqueue (idempotent), get, await, withdraw and participant respond, with
// first-terminal-wins and expiry enforced by the store owner.
//
// Presentation is out of scope: an enqueued item starts in state presented
// (revision 1) and is respondable at once.
//
// Expiry (D4): when a request carries expires_at, a write (Respond or
// Withdraw) arriving at or after that instant first materializes the expired
// outcome with a compare-and-set, so a late reply is refused atomically even
// if ExpireDue has never run. ExpireDue is the sweeper entry point; nothing
// depends on one running.
//
// Get and Await never mutate. Past expires_at they report the logically
// correct expired view, computed on the fly from the stored record and the
// clock, and write nothing: no Store write, no revision bump. The computed view
// is exactly what a later write persists (revision+1, terminated_at =
// expires_at), so it does not change when it is later materialized. Only
// Respond, Withdraw and ExpireDue materialize expiry, exactly once.
type Service struct {
	store Store
	opts  Options

	mu       sync.Mutex
	watchers map[string]*watcher
}

type watcher struct {
	ch   chan struct{}
	refs int
}

// NewService returns a Service over store.
func NewService(store Store, opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = func() string { return randomID("item_") }
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 250 * time.Millisecond
	}
	return &Service{store: store, opts: opts, watchers: map[string]*watcher{}}
}

func randomID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("hitl: crypto/rand failed: " + err.Error())
	}
	return prefix + hex.EncodeToString(b[:])
}

func (s *Service) now() time.Time { return s.opts.Clock().UTC() }

// Enqueue records a request and returns its handle. The same idempotency key
// from the same source application with identical content returns the
// original item's current handle; different content is an
// *IdempotencyConflictError naming the existing item and changes nothing.
func (s *Service) Enqueue(ctx context.Context, req EnqueueRequest) (Handle, error) {
	if err := req.Validate(); err != nil {
		return Handle{}, err
	}
	if req.ExpiresAt != nil {
		t := req.ExpiresAt.UTC()
		req.ExpiresAt = &t
	}
	snapshot, digest, err := canonicalize(req)
	if err != nil {
		return Handle{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	now := s.now()
	rec := Record{
		ItemID: s.opts.NewID(), CallerScope: req.Source.ApplicationID, IdempotencyKey: req.IdempotencyKey,
		Digest: digest, Kind: req.Kind, Request: snapshot, State: StatePresented, Revision: 1,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: req.ExpiresAt,
	}
	got, created, err := s.store.Create(ctx, rec)
	if err != nil {
		return Handle{}, err
	}
	if !created && got.Digest != digest {
		return Handle{}, &IdempotencyConflictError{IdempotencyKey: req.IdempotencyKey, ExistingItemID: got.ItemID}
	}
	cur, err := s.peek(ctx, got.ItemID, now)
	if err != nil {
		return Handle{}, err
	}
	return Handle{ContractVersion: ContractVersion, ItemID: cur.ItemID, State: cur.State, Revision: cur.Revision}, nil
}

// canonicalize returns the canonical JSON of req (object keys sorted, array
// order kept) and the sha256 digest of the same JSON without idempotency_key.
func canonicalize(req EnqueueRequest) (snapshot []byte, digest string, err error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err = dec.Decode(&m); err != nil {
		return nil, "", err
	}
	snapshot, err = json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	delete(m, "idempotency_key")
	body, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return snapshot, hex.EncodeToString(sum[:]), nil
}

func lapsed(rec Record, now time.Time) bool {
	return !rec.State.IsTerminal() && rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt)
}

// expire tries to move a lapsed record to expired. won reports that this call
// performed the transition.
func (s *Service) expire(ctx context.Context, rec Record) (Record, bool, error) {
	next := expiredView(rec)
	if err := s.store.Swap(ctx, rec.ItemID, rec.Revision, next); err != nil {
		return rec, false, err
	}
	s.notify(rec.ItemID)
	return next, true, nil
}

// expiredView returns what expire would persist, without persisting it.
func expiredView(rec Record) Record {
	next := rec
	next.State = StateExpired
	next.Revision = rec.Revision + 1
	next.UpdatedAt = *rec.ExpiresAt
	next.Outcome = Expired{ItemID: rec.ItemID, InteractionRevision: next.Revision, PolicyRef: ExpiryPolicyRef, TerminatedAt: *rec.ExpiresAt}
	return next
}

// peek loads a record for a read. If it has lapsed at now it returns the
// computed expired view; it never writes.
func (s *Service) peek(ctx context.Context, itemID string, now time.Time) (Record, error) {
	rec, err := s.store.Get(ctx, itemID)
	if err != nil {
		return Record{}, err
	}
	if lapsed(rec, now) {
		return expiredView(rec), nil
	}
	return rec, nil
}

// current loads a record for a write, first materializing expiry if it has lapsed at now.
func (s *Service) current(ctx context.Context, itemID string, now time.Time) (Record, error) {
	for range maxAttempts {
		rec, err := s.store.Get(ctx, itemID)
		if err != nil {
			return Record{}, err
		}
		if !lapsed(rec, now) {
			return rec, nil
		}
		next, _, err := s.expire(ctx, rec)
		if err == nil {
			return next, nil
		}
		if errors.Is(err, ErrRevisionMismatch) || errors.Is(err, ErrTerminalRecord) {
			continue
		}
		return Record{}, err
	}
	return Record{}, fmt.Errorf("hitl: item %q kept changing while materializing expiry", itemID)
}

func (s *Service) scoped(ctx context.Context, itemID string, caller CallerAssertion, now time.Time, write bool) (Record, error) {
	load := s.peek
	if write {
		load = s.current
	}
	rec, err := load(ctx, itemID, now)
	if err != nil {
		return Record{}, err
	}
	if rec.CallerScope != caller.ApplicationID {
		return Record{}, ErrNotFound // do not disclose another caller's item
	}
	return rec, nil
}

func view(rec Record) ItemView {
	return ItemView{
		ContractVersion: ContractVersion, ItemID: rec.ItemID, State: rec.State, Revision: rec.Revision,
		RequestSnapshot: rec.Request, EnqueuedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, TerminalOutcome: rec.Outcome,
	}
}

// Get returns the current projection of one item.
func (s *Service) Get(ctx context.Context, cmd GetCommand) (RetrievalResult, error) {
	if err := cmd.Validate(); err != nil {
		return RetrievalResult{}, err
	}
	now := s.now()
	rec, err := s.scoped(ctx, cmd.ItemID, cmd.Caller, now, false)
	if err != nil {
		return RetrievalResult{}, err
	}
	return RetrievalResult{ContractVersion: ContractVersion, Mode: ModeGet, WaitStatus: WaitNotWaited, RetrievedAt: now, Item: view(rec)}, nil
}

// Await waits until the item is terminal or the wait elapses, whichever is
// first. Ending the wait (timeout, context cancellation) changes nothing about
// the item. A context error is returned as is.
func (s *Service) Await(ctx context.Context, cmd AwaitCommand) (RetrievalResult, error) {
	if err := cmd.Validate(); err != nil {
		return RetrievalResult{}, err
	}
	wait := cmd.Wait()
	timeout := time.NewTimer(wait)
	defer timeout.Stop()
	tick := time.NewTicker(s.opts.PollInterval)
	defer tick.Stop()
	ch, release := s.subscribe(cmd.ItemID)
	defer func() { release() }()
	result := func(rec Record, now time.Time) RetrievalResult {
		status := WaitTimeout
		if rec.State.IsTerminal() {
			status = WaitTerminal
		}
		return RetrievalResult{ContractVersion: ContractVersion, Mode: ModeAwait, WaitStatus: status, RetrievedAt: now, Item: view(rec)}
	}
	for {
		now := s.now()
		rec, err := s.scoped(ctx, cmd.ItemID, cmd.Caller, now, false)
		if err != nil {
			return RetrievalResult{}, err
		}
		if rec.State.IsTerminal() || wait == 0 {
			return result(rec, now), nil
		}
		select {
		case <-ch:
			release()
			ch, release = s.subscribe(cmd.ItemID)
		case <-tick.C:
		case <-timeout.C:
			now = s.now()
			rec, err = s.scoped(ctx, cmd.ItemID, cmd.Caller, now, false)
			if err != nil {
				return RetrievalResult{}, err
			}
			return result(rec, now), nil
		case <-ctx.Done():
			return RetrievalResult{}, ctx.Err()
		}
	}
}

func (s *Service) subscribe(itemID string) (<-chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.watchers[itemID]
	if w == nil {
		w = &watcher{ch: make(chan struct{})}
		s.watchers[itemID] = w
	}
	w.refs++
	var once sync.Once
	return w.ch, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			w.refs--
			if w.refs == 0 && s.watchers[itemID] == w {
				delete(s.watchers, itemID)
			}
		})
	}
}

func (s *Service) notify(itemID string) {
	s.mu.Lock()
	w := s.watchers[itemID]
	delete(s.watchers, itemID)
	s.mu.Unlock()
	if w != nil {
		close(w.ch)
	}
}

func staleFrom(op, itemID string, expected int64, rec Record) *StaleRevisionError {
	return &StaleRevisionError{
		Operation: op, ItemID: itemID, RevisionKind: "interaction",
		Expected: expected, Actual: rec.Revision, CurrentState: rec.State, Outcome: rec.Outcome,
	}
}

// Withdraw cancels a nonterminal item with cause caller_withdrawn and returns
// the outcome. Repeating a withdrawal returns the same outcome; any other
// terminal winner yields a *TerminalConflictError carrying that outcome.
// ExpectedRevision, when set, must match or the result is a
// *StaleRevisionError.
func (s *Service) Withdraw(ctx context.Context, cmd WithdrawCommand) (Outcome, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	now := s.now()
	for range maxAttempts {
		rec, err := s.scoped(ctx, cmd.ItemID, cmd.Caller, now, true)
		if err != nil {
			return nil, err
		}
		if rec.State.IsTerminal() {
			if c, ok := rec.Outcome.(Canceled); ok && c.Cause == CauseCallerWithdrawn {
				return c, nil
			}
			return nil, &TerminalConflictError{Outcome: rec.Outcome}
		}
		if cmd.ExpectedRevision != nil && *cmd.ExpectedRevision != rec.Revision {
			return nil, staleFrom("withdraw", cmd.ItemID, *cmd.ExpectedRevision, rec)
		}
		next := rec
		next.State = StateCanceled
		next.Revision = rec.Revision + 1
		next.UpdatedAt = now
		out := Canceled{ItemID: rec.ItemID, InteractionRevision: next.Revision, Cause: CauseCallerWithdrawn, Reason: cmd.Reason, TerminatedAt: now}
		next.Outcome = out
		err = s.store.Swap(ctx, rec.ItemID, rec.Revision, next)
		if err == nil {
			s.notify(rec.ItemID)
			return out, nil
		}
		if errors.Is(err, ErrRevisionMismatch) || errors.Is(err, ErrTerminalRecord) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("hitl: item %q kept changing during withdraw", cmd.ItemID)
}

// RespondCommand is a participant's answer. Resolving is not a wire verb of
// the contract (it is presentation-bound); it is the entry point a
// presentation channel calls on a Service.
type RespondCommand struct {
	ItemID string
	// ExpectedRevision, when set, must equal the item's revision.
	ExpectedRevision *int64
	Response         Response
	// Participant says who answered, how assured, and optionally with what
	// proof. Service records it; it does not verify a Proof.
	Participant Participant
}

// Respond resolves a respondable item with the participant's answer and
// returns the resolved outcome. The item must be presented or in_progress,
// response.kind must equal the request kind, and the answer must be given
// before expires_at. If another terminal outcome exists (including expiry,
// whether or not a sweeper has run) the result is a *TerminalConflictError
// carrying it; a mismatched ExpectedRevision is a *StaleRevisionError.
func (s *Service) Respond(ctx context.Context, cmd RespondCommand) (Outcome, error) {
	if cmd.ItemID == "" {
		return nil, fmt.Errorf("%w: item_id is required", ErrInvalidRequest)
	}
	if cmd.ExpectedRevision != nil && *cmd.ExpectedRevision < 1 {
		return nil, fmt.Errorf("%w: expected_revision must be positive", ErrInvalidRequest)
	}
	if err := cmd.Response.validate(); err != nil {
		return nil, err
	}
	if err := cmd.Participant.Validate(); err != nil {
		return nil, err
	}
	now := s.now() // the one reading that decides expiry and stamps resolved_at
	for range maxAttempts {
		rec, err := s.current(ctx, cmd.ItemID, now)
		if err != nil {
			return nil, err
		}
		if cmd.ExpectedRevision != nil && *cmd.ExpectedRevision != rec.Revision {
			return nil, staleFrom("resolve", cmd.ItemID, *cmd.ExpectedRevision, rec)
		}
		if rec.State.IsTerminal() {
			return nil, &TerminalConflictError{Outcome: rec.Outcome}
		}
		if rec.State != StatePresented && rec.State != StateInProgress {
			return nil, fmt.Errorf("%w: item %q is %s and not respondable", ErrInvalidRequest, rec.ItemID, rec.State)
		}
		if cmd.Response.Kind != rec.Kind {
			return nil, fmt.Errorf("%w: response kind %q does not match request kind %q", ErrInvalidRequest, cmd.Response.Kind, rec.Kind)
		}
		if s.opts.ValidateResponse != nil {
			if verr := s.opts.ValidateResponse(rec.Kind, cmd.Response); verr != nil {
				return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, verr)
			}
		}
		next := rec
		next.State = StateResolved
		next.Revision = rec.Revision + 1
		next.UpdatedAt = now
		out := Resolved{ItemID: rec.ItemID, InteractionRevision: next.Revision, Resolution: ResolutionRecord{
			ResolutionID: randomID("resolution_"), Response: cmd.Response, Participant: cmd.Participant,
			ResolvedAt: now, InteractionRevision: next.Revision,
		}}
		next.Outcome = out
		err = s.store.Swap(ctx, rec.ItemID, rec.Revision, next)
		if err == nil {
			s.notify(rec.ItemID)
			return out, nil
		}
		if errors.Is(err, ErrRevisionMismatch) || errors.Is(err, ErrTerminalRecord) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("hitl: item %q kept changing during respond", cmd.ItemID)
}

// ExpireDue moves every lapsed nonterminal item to expired and returns how
// many this call expired. It is optional: expiry is also enforced on every
// operation, so a sweeper only makes it prompt (waiters wake, Get reflects it).
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	now := s.now()
	total := 0
	for {
		batch, err := s.store.DueForExpiry(ctx, now, 100)
		if err != nil {
			return total, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		progressed := false
		for _, rec := range batch {
			_, won, err := s.expire(ctx, rec)
			switch {
			case err == nil && won:
				total++
				progressed = true
			case errors.Is(err, ErrRevisionMismatch), errors.Is(err, ErrTerminalRecord):
				// lost to another writer; it is terminal or has moved on
				progressed = true
			case err != nil:
				return total, err
			}
		}
		if !progressed {
			return total, nil
		}
	}
}
