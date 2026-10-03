package memstore

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/hollis-labs/substrate/mesh/hitl"
)

// Store is an in-memory hitl.Store. The zero value is not usable; call New.
type Store struct {
	mu    sync.Mutex
	items map[string]hitl.Record
	byKey map[[2]string]string
}

var _ hitl.Store = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{items: map[string]hitl.Record{}, byKey: map[[2]string]string{}}
}

// clone detaches the mutable parts of a record (the request bytes) from the
// caller. Outcomes are immutable values and are shared.
func clone(r hitl.Record) hitl.Record {
	r.Request = bytes.Clone(r.Request)
	if r.ExpiresAt != nil {
		t := *r.ExpiresAt
		r.ExpiresAt = &t
	}
	return r
}

// Create implements hitl.Store.
func (s *Store) Create(ctx context.Context, rec hitl.Record) (hitl.Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return hitl.Record{}, false, err
	}
	if err := rec.Validate(); err != nil {
		return hitl.Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]string{rec.CallerScope, rec.IdempotencyKey}
	if id, ok := s.byKey[key]; ok {
		return clone(s.items[id]), false, nil
	}
	if _, dup := s.items[rec.ItemID]; dup {
		return hitl.Record{}, false, hitl.ErrInvalidRecord
	}
	rec = clone(rec)
	s.items[rec.ItemID] = rec
	s.byKey[key] = rec.ItemID
	return clone(rec), true, nil
}

// Get implements hitl.Store.
func (s *Store) Get(ctx context.Context, itemID string) (hitl.Record, error) {
	if err := ctx.Err(); err != nil {
		return hitl.Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.items[itemID]
	if !ok {
		return hitl.Record{}, hitl.ErrNotFound
	}
	return clone(rec), nil
}

// Swap implements hitl.Store.
func (s *Store) Swap(ctx context.Context, itemID string, expected int64, next hitl.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.items[itemID]
	if !ok {
		return hitl.ErrNotFound
	}
	if err := hitl.CheckSwap(prev, expected, next); err != nil {
		return err
	}
	s.items[itemID] = clone(next)
	return nil
}

// DueForExpiry implements hitl.Store.
func (s *Store) DueForExpiry(ctx context.Context, now time.Time, limit int) ([]hitl.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []hitl.Record
	for _, r := range s.items {
		if !r.State.IsTerminal() && r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
			due = append(due, clone(r))
		}
	}
	slices.SortFunc(due, func(a, b hitl.Record) int {
		if c := a.ExpiresAt.Compare(*b.ExpiresAt); c != 0 {
			return c
		}
		return bytes.Compare([]byte(a.ItemID), []byte(b.ItemID))
	})
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}
