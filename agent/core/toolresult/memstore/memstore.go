package memstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	toolresult "github.com/hollis-labs/go-toolresult"
)

// Store is an in-memory [toolresult.Store].
type Store struct {
	mu      sync.Mutex
	entries map[string]toolresult.Entry
}

var _ toolresult.Store = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{entries: make(map[string]toolresult.Entry)}
}

// Put inserts e, truncating its times to whole seconds like the SQL store. It
// fails if the id exists.
func (s *Store) Put(ctx context.Context, e toolresult.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.CreatedAt, e.ExpiresAt = e.CreatedAt.UTC().Truncate(time.Second), e.ExpiresAt.UTC().Truncate(time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.entries[e.ID]; dup {
		return fmt.Errorf("memstore: duplicate id %q", e.ID)
	}
	s.entries[e.ID] = e
	return nil
}

// Get returns the entry with that id owned by scope, or
// [toolresult.ErrNotFound].
func (s *Store) Get(ctx context.Context, scope, id string) (toolresult.Entry, error) {
	if err := ctx.Err(); err != nil {
		return toolresult.Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok || e.Scope != scope {
		return toolresult.Entry{}, toolresult.ErrNotFound
	}
	return e, nil
}

// DeleteExpired removes entries that expire before the given time.
func (s *Store) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	before = before.UTC().Truncate(time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, e := range s.entries {
		if e.ExpiresAt.Before(before) {
			delete(s.entries, id)
			n++
		}
	}
	return n, nil
}
