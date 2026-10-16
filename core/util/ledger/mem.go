package ledger

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory Store for tests and local use.
type MemStore struct {
	Now func() time.Time

	mu    sync.Mutex
	byID  map[string]*Entry
	byKey map[string]string
	order []string
}

func (m *MemStore) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MemStore) init() {
	if m.byID == nil {
		m.byID, m.byKey = map[string]*Entry{}, map[string]string{}
	}
}

// Append stores r unless its idempotency key is already present, in which case it returns the stored record.
func (m *MemStore) Append(_ context.Context, r Record) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if id, ok := m.byKey[r.IdempotencyKey]; ok && r.IdempotencyKey != "" {
		return clone(*m.byID[id]).Record, true, nil
	}
	if r.DecisionID == "" {
		r.DecisionID = NewID()
	}
	if err := r.Validate(); err != nil {
		return Record{}, false, errInvalid(err)
	}
	if _, dup := m.byID[r.DecisionID]; dup {
		return Record{}, false, errInvalid(ErrInvalid)
	}
	r.CreatedAt = m.now().UTC()
	m.byID[r.DecisionID] = &Entry{Record: clone(Entry{Record: r}).Record}
	m.byKey[r.IdempotencyKey] = r.DecisionID
	m.order = append(m.order, r.DecisionID)
	return clone(Entry{Record: r}).Record, false, nil
}

type invalid struct{ err error }

func (e invalid) Error() string   { return "ledger: invalid: " + e.err.Error() }
func (e invalid) Unwrap() []error { return []error{ErrInvalid, e.err} }
func errInvalid(err error) error  { return invalid{err} }

// AppendEvent validates e, checks disposition moves and numbers the event.
func (m *MemStore) AppendEvent(_ context.Context, e Event) (Event, error) {
	if err := e.validate(); err != nil {
		return Event{}, errInvalid(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	ent, ok := m.byID[e.DecisionID]
	if !ok {
		return Event{}, ErrNotFound
	}
	if e.Kind == EventDisposition && !canMove(ent.Current(), e.Disposition) {
		return Event{}, ErrTransition
	}
	e.Seq = len(ent.Events) + 1
	e.At = m.now().UTC()
	ent.Events = append(ent.Events, e)
	return e, nil
}

// Get returns the entry and its events.
func (m *MemStore) Get(_ context.Context, id string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ent, ok := m.byID[id]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return clone(*ent), nil
}

// clone deep-copies the parts a caller could mutate.
func clone(e Entry) Entry {
	r := &e.Record
	r.Alternatives = append([]Alternative(nil), r.Alternatives...)
	r.Input.Snapshot = append([]byte(nil), r.Input.Snapshot...)
	r.Output.Detail = append([]byte(nil), r.Output.Detail...)
	if r.Output.Probabilities != nil {
		p := make(map[string]float64, len(r.Output.Probabilities))
		for k, v := range r.Output.Probabilities {
			p[k] = v
		}
		r.Output.Probabilities = p
	}
	evs := make([]Event, len(e.Events))
	for i, ev := range e.Events {
		if ev.Correction != nil {
			c := *ev.Correction
			ev.Correction = &c
		}
		if ev.Outcome != nil {
			o := *ev.Outcome
			ev.Outcome = &o
		}
		evs[i] = ev
	}
	e.Events = evs
	return e
}

// Query returns entries newest first.
func (m *MemStore) Query(_ context.Context, f Filter) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var out []Entry
	for _, id := range m.order {
		e := *m.byID[id]
		switch {
		case f.Stage != "" && e.Record.Stage != f.Stage,
			f.SubjectRef != "" && e.Record.SubjectRef != f.SubjectRef,
			!f.Since.IsZero() && e.Record.CreatedAt.Before(f.Since):
			continue
		}
		if f.Corrected != nil {
			if _, c := e.Label(); c != *f.Corrected {
				continue
			}
		}
		out = append(out, clone(e))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Record.CreatedAt.After(out[j].Record.CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ExpireInputs drops snapshots past RawExpiresAt that are not marked Keep,
// leaving the hash and every other field.
func (m *MemStore) ExpireInputs(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.byID {
		in := &e.Record.Input
		if in.Mode == ModeSnapshot && !in.Keep && !in.Expired && in.RawExpiresAt != nil && !in.RawExpiresAt.After(now) {
			in.Snapshot, in.Expired = nil, true
			n++
		}
	}
	return n, nil
}

var _ interface {
	Store
	Expirer
} = (*MemStore)(nil)
