package agentdir

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
)

// MemoryStore is a reference projection for tests and standalone examples.
// Production hosts must provide a durable Store and authoritative input lease.
type MemoryStore struct {
	mu     sync.Mutex
	items  map[conversationKey]*memoryConversation
	closed map[string]bool
}

type conversationKey struct {
	kind       Kind
	scope      string
	definition string
}

type memoryConversation struct {
	id         string
	definition string
	busy       bool
	cancel     context.CancelFunc
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: map[conversationKey]*memoryConversation{}, closed: map[string]bool{}}
}

func keyFor(s Snapshot) conversationKey {
	p := s.Profile
	switch p.Kind {
	case Stable:
		return conversationKey{kind: p.Kind, scope: p.SeatID}
	case Task:
		return conversationKey{kind: p.Kind, scope: p.TaskID}
	default:
		return conversationKey{kind: p.Kind, scope: p.ApplicationID, definition: s.Definition.DefinitionID}
	}
}

type memoryLease struct {
	store *MemoryStore
	key   conversationKey
	id    string
	ctx   context.Context
	once  sync.Once
}

func (l *memoryLease) ConversationID() string   { return l.id }
func (l *memoryLease) Context() context.Context { return l.ctx }
func (l *memoryLease) Release() {
	l.once.Do(func() {
		l.store.mu.Lock()
		defer l.store.mu.Unlock()
		entry := l.store.items[l.key]
		entry.busy = false
		entry.cancel()
		entry.cancel = nil
	})
}

func (s *MemoryStore) Acquire(ctx context.Context, snap Snapshot) (Lease, error) {
	if s == nil {
		return nil, errors.New("agentdir: nil store")
	}
	if err := snap.Profile.Validate(&snap.Definition); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap.Profile.Kind == Task && s.closed[snap.Profile.TaskID] {
		return nil, ErrTaskEnded
	}
	key := keyFor(snap)
	entry := s.items[key]
	if entry == nil {
		entry = &memoryConversation{id: "conv_" + rand.Text(), definition: snap.Definition.DefinitionID}
		s.items[key] = entry
	}
	if entry.definition != snap.Definition.DefinitionID {
		return nil, fmt.Errorf("%w: seat or task definition ID changed", ErrProfile)
	}
	if entry.busy {
		return nil, ErrBusy
	}
	entry.busy = true
	leaseCtx, cancel := context.WithCancel(ctx)
	entry.cancel = cancel
	return &memoryLease{store: s, key: key, id: entry.id, ctx: leaseCtx}, nil
}

func (s *MemoryStore) EndTask(ctx context.Context, taskID string) error {
	if s == nil || taskID == "" {
		return fmt.Errorf("agentdir: store and task ID required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed[taskID] = true
	for key, entry := range s.items {
		if key.kind == Task && key.scope == taskID && entry.cancel != nil {
			entry.cancel()
		}
	}
	return nil
}
