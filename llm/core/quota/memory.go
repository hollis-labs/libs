package quota

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore keeps usage in process memory. It is a Recorder: committed
// reservations are recorded in it. Usage is lost when the process exits; use
// SeededStore to start from the application's history, or SQLStore to read it
// on every decision.
//
// It never drops entries by itself. Forget removes old ones when the caller
// decides they can go.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[Counter][]Entry // per counter, sorted by At
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: map[Counter][]Entry{}}
}

// Record adds e to c.
func (m *MemoryStore) Record(_ context.Context, c Counter, e Entry) error {
	if e.Amount < 0 {
		return ErrNegativeAmount
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertLocked(c, e)
	return nil
}

func (m *MemoryStore) insertLocked(c Counter, e Entry) {
	es := m.entries[c]
	i := sort.Search(len(es), func(i int) bool { return es[i].At.After(e.At) })
	es = append(es, Entry{})
	copy(es[i+1:], es[i:])
	es[i] = e
	m.entries[c] = es
}

// rangeLocked returns the entries of c with from <= At < to, sharing the
// store's backing array.
func (m *MemoryStore) rangeLocked(c Counter, from, to time.Time) []Entry {
	es := m.entries[c]
	lo := sort.Search(len(es), func(i int) bool { return !es[i].At.Before(from) })
	hi := sort.Search(len(es), func(i int) bool { return !es[i].At.Before(to) })
	if lo >= hi {
		return nil
	}
	return es[lo:hi]
}

// Sum implements Store.
func (m *MemoryStore) Sum(_ context.Context, c Counter, from, to time.Time) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return total(m.rangeLocked(c, from, to)), nil
}

// Entries implements Store. The returned slice is a copy.
func (m *MemoryStore) Entries(_ context.Context, c Counter, from, to time.Time) ([]Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Entry(nil), m.rangeLocked(c, from, to)...), nil
}

// Forget removes every entry dated before before, from every counter. Call it
// only with a time older than the start of the longest window still in use;
// an entry inside a window that is still counted must not be forgotten.
func (m *MemoryStore) Forget(before time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for c, es := range m.entries {
		i := sort.Search(len(es), func(i int) bool { return !es[i].At.Before(before) })
		if i == len(es) {
			delete(m.entries, c)
		} else if i > 0 {
			m.entries[c] = append([]Entry(nil), es[i:]...)
		}
	}
}
