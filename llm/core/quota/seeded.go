package quota

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// SeedEntry is one past usage event the application reports when a
// SeededStore seeds itself.
type SeedEntry struct {
	Counter Counter
	Entry   Entry
}

// SeedFunc reads the application's own record of past usage (an audit log, an
// events table) and returns every event dated at or after since. It is how a
// restarted process counts what already ran instead of starting from zero.
type SeedFunc func(ctx context.Context, since time.Time) ([]SeedEntry, error)

// SeededStore is a MemoryStore that fills itself from the application's
// history the first time it is read, and again after Reseed. Between seeds it
// records committed reservations in memory, like MemoryStore; the application
// keeps writing its own record as usual, and the next seed reads it back.
//
// This is the pattern for a process that gates calls and also writes an audit
// log of them: the log is the history, the store is a fast copy of the part of
// it the limits look at.
type SeededStore struct {
	seed    SeedFunc
	horizon time.Duration
	clock   Clock

	mu     sync.Mutex // serializes seeding
	seeded bool
	mem    *MemoryStore
}

// NewSeededStore returns a store seeded by seed with the events of the last
// horizon, which should be at least the longest window any limit on it uses.
// A nil clock means the system clock.
func NewSeededStore(seed SeedFunc, horizon time.Duration, clock Clock) *SeededStore {
	if clock == nil {
		clock = systemClock{}
	}
	return &SeededStore{seed: seed, horizon: horizon, clock: clock, mem: NewMemoryStore()}
}

// Reseed discards the in-memory copy; the next read seeds again. Call it when
// the set of limits changes in a way that needs history the last seed did not
// read, or to pick up events another process wrote.
func (s *SeededStore) Reseed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seeded = false
}

func (s *SeededStore) ensure(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seeded {
		return nil
	}
	mem := NewMemoryStore()
	if s.seed != nil {
		got, err := s.seed(ctx, s.clock.Now().Add(-s.horizon))
		if err != nil {
			// Stay unseeded so the next read tries again.
			return fmt.Errorf("quota: seed: %w", err)
		}
		for _, e := range got {
			if e.Entry.Amount < 0 {
				return fmt.Errorf("quota: seed: %s: %w", e.Counter, ErrNegativeAmount)
			}
			mem.insertLocked(e.Counter, e.Entry)
		}
	}
	s.mem, s.seeded = mem, true
	return nil
}

func (s *SeededStore) current(ctx context.Context) (*MemoryStore, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem, nil
}

// Record implements Recorder. It seeds first, so a record made before the first
// read is not lost when the seed replaces the in-memory copy.
func (s *SeededStore) Record(ctx context.Context, c Counter, e Entry) error {
	m, err := s.current(ctx)
	if err != nil {
		return err
	}
	return m.Record(ctx, c, e)
}

// Sum implements Store.
func (s *SeededStore) Sum(ctx context.Context, c Counter, from, to time.Time) (int64, error) {
	m, err := s.current(ctx)
	if err != nil {
		return 0, err
	}
	return m.Sum(ctx, c, from, to)
}

// Entries implements Store.
func (s *SeededStore) Entries(ctx context.Context, c Counter, from, to time.Time) ([]Entry, error) {
	m, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return m.Entries(ctx, c, from, to)
}
