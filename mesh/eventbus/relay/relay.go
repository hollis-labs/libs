// Package relay ships an app's local event outbox to the hub through an
// eventbus client.
//
// An app that cannot afford to lose events writes them to its own outbox in the
// same transaction as the change they describe. The outbox is the source of
// truth. The relay reads it in order from a persisted cursor, hands each batch
// to [eventbus.Client.Deliver], and moves the cursor forward only over events
// the hub has settled: accepted, already had, or set aside durably in the
// client's quarantine. Producers push; the hub never pulls.
//
// Delivery is at least once. A crash, or a cursor that could not be saved, makes
// the relay read some events again after a restart; the hub dedupes on the
// event id and answers "duplicate", so nothing is lost and nothing is applied
// twice. The relay never skips an event it has not seen settled.
//
// The relay handles one batch at a time (Config.BatchSize entries), so what is in
// flight is bounded by it. A missing or expired credential does not drain the
// outbox into quarantine: Deliver waits for it, and the outbox, being durable,
// loses nothing while it waits. Close the client to stop a relay that is waiting;
// Close coordinates with the running Deliver.
//
// An app plugs in two small things: a [Source] over its outbox (a method that
// returns the entries after a sequence number) and a [Cursor] (a place to keep
// the last settled sequence number; [FileCursor] is one).
package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/libs/mesh/eventbus"
)

// Entry is one outbox row: a sequence number that increases with every row, and
// the event to send.
type Entry struct {
	Seq  int64
	Item eventbus.Item
}

// Source reads the outbox. After returns up to limit entries whose Seq is
// greater than after, in increasing Seq order. An empty result means nothing is
// waiting.
type Source interface {
	After(ctx context.Context, after int64, limit int) ([]Entry, error)
}

// Cursor keeps the last sequence number the hub has settled.
type Cursor interface {
	// Load returns the saved sequence number, or 0 if none was saved.
	Load(ctx context.Context) (int64, error)
	// Save records that everything up to and including seq is settled.
	Save(ctx context.Context, seq int64) error
}

// HeadSource is an optional extension of [Source]: the highest sequence number
// in the outbox. With it the relay reports how far behind it is (Stats.Lag).
type HeadSource interface {
	Head(ctx context.Context) (int64, error)
}

// Deliverer sends a batch and reports what became of each item. It is
// satisfied by *eventbus.Client.
type Deliverer interface {
	Deliver(ctx context.Context, items []eventbus.Item) []eventbus.Result
}

// Config configures a [Relay]. Source, Cursor and Deliverer are required.
type Config struct {
	Source    Source
	Cursor    Cursor
	Deliverer Deliverer

	// BatchSize is how many entries are read at a time. Default 100.
	BatchSize int
	// Poll is how long to wait when the outbox is empty before looking again.
	// [Relay.Wake] cuts the wait short. Default 1s.
	Poll time.Duration
	// ErrorBackoff is the first wait after a failure of the source or of a
	// batch that could not be settled; it doubles up to MaxBackoff. Defaults
	// 500ms and 30s.
	ErrorBackoff time.Duration
	MaxBackoff   time.Duration

	// OnError reports failures the relay is retrying: a source that failed, a
	// cursor that could not be saved, a source that returned entries out of
	// order. It is never called with a lock held and must not block.
	OnError func(error)
}

// Stats is a snapshot of the relay's counters.
type Stats struct {
	Cursor int64 // last sequence number settled
	// Head is the highest outbox sequence number seen when the source can say
	// (HeadSource), else 0; Lag is Head - Cursor, never below 0.
	Head, Lag int64
	// Sent counts entries the hub accepted or already had; Duplicates is the
	// part of Sent the hub already had. Rejected counts entries the hub
	// rejected, and Quarantined every entry set aside undelivered (rejected
	// ones included); the relay moves past those once they are durably
	// recorded.
	Sent, Duplicates, Rejected, Quarantined uint64
	Batches                                 uint64
	Errors                                  uint64
}

// Relay moves outbox entries to the hub.
type Relay struct {
	cfg  Config
	wake chan struct{}

	cursor      atomic.Int64
	head        atomic.Int64
	sent        atomic.Uint64
	dups        atomic.Uint64
	rejected    atomic.Uint64
	quarantined atomic.Uint64
	batches     atomic.Uint64
	errs        atomic.Uint64

	mu      sync.Mutex
	running bool
}

// New checks cfg and returns a Relay. It does not start anything.
func New(cfg Config) (*Relay, error) {
	if cfg.Source == nil || cfg.Cursor == nil || cfg.Deliverer == nil {
		return nil, errors.New("relay: Source, Cursor and Deliverer are required")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Poll <= 0 {
		cfg.Poll = time.Second
	}
	if cfg.ErrorBackoff <= 0 {
		cfg.ErrorBackoff = 500 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	return &Relay{cfg: cfg, wake: make(chan struct{}, 1)}, nil
}

// Wake makes an idle relay look at the outbox now. Call it after writing to the
// outbox if you want low latency; without it the relay looks every Poll.
func (r *Relay) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Stats returns the counters.
func (r *Relay) Stats() Stats {
	st := Stats{
		Cursor:      r.cursor.Load(),
		Head:        r.head.Load(),
		Sent:        r.sent.Load(),
		Duplicates:  r.dups.Load(),
		Rejected:    r.rejected.Load(),
		Quarantined: r.quarantined.Load(),
		Batches:     r.batches.Load(),
		Errors:      r.errs.Load(),
	}
	if st.Head > st.Cursor {
		st.Lag = st.Head - st.Cursor
	}
	return st
}

// refreshHead updates the head from the source when it can report one.
func (r *Relay) refreshHead(ctx context.Context) {
	hs, ok := r.cfg.Source.(HeadSource)
	if !ok {
		return
	}
	if h, err := hs.Head(ctx); err == nil {
		r.head.Store(h)
	}
}

func (r *Relay) report(err error) {
	r.errs.Add(1)
	if r.cfg.OnError != nil {
		r.cfg.OnError(err)
	}
}

// wait sleeps for d, or until ctx is done or Wake is called. It reports whether
// ctx is still live.
func (r *Relay) wait(ctx context.Context, d time.Duration, wakeable bool) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	var wake <-chan struct{}
	if wakeable {
		wake = r.wake
	}
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-wake:
	}
	return ctx.Err() == nil
}

// Run relays until ctx is done, then returns ctx's error. It returns another
// error only if the saved cursor cannot be read, since starting from the wrong
// place could skip events or resend all of them. Run may be called once at a
// time.
func (r *Relay) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("relay: already running")
	}
	r.running = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.running = false; r.mu.Unlock() }()

	cursor, err := r.cfg.Cursor.Load(ctx)
	if err != nil {
		return fmt.Errorf("relay: load cursor: %w", err)
	}
	r.cursor.Store(cursor)
	saved := cursor // the last value known to be stored
	backoff := r.cfg.ErrorBackoff

	save := func(seq int64) {
		if seq == saved {
			return
		}
		sctx := ctx
		if ctx.Err() != nil { // still try once on the way out
			var cancel context.CancelFunc
			sctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		if err := r.cfg.Cursor.Save(sctx, seq); err != nil {
			r.report(fmt.Errorf("relay: save cursor %d (will retry; a restart would resend, which the hub tolerates): %w", seq, err))
			return
		}
		saved = seq
	}

	for {
		if ctx.Err() != nil {
			save(cursor)
			return ctx.Err()
		}
		r.refreshHead(ctx)
		entries, err := r.cfg.Source.After(ctx, cursor, r.cfg.BatchSize)
		if err != nil {
			if ctx.Err() == nil {
				r.report(fmt.Errorf("relay: read outbox after %d: %w", cursor, err))
			}
			if !r.wait(ctx, backoff, false) {
				save(cursor)
				return ctx.Err()
			}
			backoff = next(backoff, r.cfg.MaxBackoff)
			continue
		}
		if len(entries) == 0 {
			save(cursor) // retry a cursor save that failed earlier
			backoff = r.cfg.ErrorBackoff
			if !r.wait(ctx, r.cfg.Poll, true) {
				return ctx.Err()
			}
			continue
		}
		if err := checkOrder(cursor, entries); err != nil {
			r.report(err)
			if !r.wait(ctx, backoff, false) {
				save(cursor)
				return ctx.Err()
			}
			backoff = next(backoff, r.cfg.MaxBackoff)
			continue
		}

		items := make([]eventbus.Item, len(entries))
		for i, e := range entries {
			items[i] = e.Item
		}
		disp := r.cfg.Deliverer.Deliver(ctx, items)
		r.batches.Add(1)

		settled := 0
		for settled < len(entries) && settled < len(disp) && disp[settled].Disposition != eventbus.DispositionPending {
			switch d := disp[settled]; d.Disposition {
			case eventbus.DispositionSent:
				r.sent.Add(1)
				if d.Duplicate {
					r.dups.Add(1)
				}
			case eventbus.DispositionQuarantined:
				r.quarantined.Add(1)
				if d.Reason == "rejected" {
					r.rejected.Add(1)
				}
			}
			settled++
		}
		if settled > 0 {
			cursor = entries[settled-1].Seq
			r.cursor.Store(cursor)
			save(cursor)
			backoff = r.cfg.ErrorBackoff
		}
		if settled < len(entries) {
			// The rest is not settled: the context ended, or the client is
			// closed. Either way do not move on.
			if ctx.Err() != nil {
				save(cursor)
				return ctx.Err()
			}
			r.report(fmt.Errorf("relay: %d of %d entries after %d are not settled (client closed?); retrying", len(entries)-settled, len(entries), cursor))
			if !r.wait(ctx, backoff, false) {
				save(cursor)
				return ctx.Err()
			}
			backoff = next(backoff, r.cfg.MaxBackoff)
		}
	}
}

func next(d, max time.Duration) time.Duration {
	if d *= 2; d > max {
		return max
	}
	return d
}

// checkOrder guards the cursor: entries must come back strictly increasing and
// all after it, or the relay could loop on the same rows or move backwards.
func checkOrder(after int64, entries []Entry) error {
	prev := after
	for _, e := range entries {
		if e.Seq <= prev {
			return fmt.Errorf("relay: source returned seq %d after %d; entries must be in increasing order and after the cursor", e.Seq, prev)
		}
		prev = e.Seq
	}
	return nil
}

// FileCursor is a [Cursor] kept in a file, replaced atomically on every Save.
type FileCursor struct{ Path string }

// Load reads the file; a missing file means 0.
func (f FileCursor) Load(context.Context) (int64, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("relay: cursor file %s does not hold a sequence number", f.Path)
	}
	return n, nil
}

// Save writes seq to a temporary file, syncs it and renames it over the cursor
// file.
func (f FileCursor) Save(_ context.Context, seq int64) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(file, "%d\n", seq); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}
