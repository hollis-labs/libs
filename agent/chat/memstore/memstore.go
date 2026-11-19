// Package memstore is the in-memory [chat.Control]: conversations,
// attachments and the input lease in process memory. It is for tests, examples
// and hosts that do not need a conversation to outlive the process. Pair it
// with streamhub.NewMemoryLog.
package memstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/internal/ctl"
)

// Control is an in-memory chat.Control. It is safe for concurrent use.
type Control struct {
	mu      sync.Mutex
	convs   map[string]*entry
	current map[string]string // agent -> conversation ID
}

type entry struct {
	conv chat.Conversation
	atts map[string]chat.Attachment
}

var (
	_ chat.Control = (*Control)(nil)
	_ chat.Bumper  = (*Control)(nil)
)

// NewControl returns an empty Control.
func NewControl() *Control {
	return &Control{convs: map[string]*entry{}, current: map[string]string{}}
}

func snapshot(c chat.Conversation) chat.Conversation {
	if c.Lease != nil {
		l := *c.Lease
		c.Lease = &l
	}
	return c
}

// do runs f on the stored conversation under the lock.
func (m *Control) do(ctx context.Context, id string, f func(e *entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.convs[id]
	if e == nil {
		return fmt.Errorf("conversation %q: %w", id, chat.ErrNotFound)
	}
	return f(e)
}

func (e *entry) att(id string) *chat.Attachment {
	a, ok := e.atts[id]
	if !ok {
		return nil
	}
	return &a
}

// Ensure implements chat.Control.
func (m *Control) Ensure(ctx context.Context, nc chat.NewConversation) (chat.Conversation, bool, error) {
	if err := ctx.Err(); err != nil {
		return chat.Conversation{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.current[nc.Agent]; ok {
		return snapshot(m.convs[id].conv), false, nil
	}
	if _, dup := m.convs[nc.ID]; dup || nc.ID == "" {
		return chat.Conversation{}, false, fmt.Errorf("memstore: conversation id %q is empty or already used", nc.ID)
	}
	e := &entry{conv: ctl.New(nc), atts: map[string]chat.Attachment{}}
	m.convs[nc.ID], m.current[nc.Agent] = e, nc.ID
	return snapshot(e.conv), true, nil
}

// Get implements chat.Control.
func (m *Control) Get(ctx context.Context, id string) (chat.Conversation, error) {
	var out chat.Conversation
	err := m.do(ctx, id, func(e *entry) error {
		out = snapshot(e.conv)
		return nil
	})
	return out, err
}

// Reset implements chat.Control.
func (m *Control) Reset(ctx context.Context, id, attachmentID string, next chat.NewConversation, now time.Time) (chat.Conversation, error) {
	var out chat.Conversation
	err := m.do(ctx, id, func(e *entry) error {
		if _, dup := m.convs[next.ID]; dup || next.ID == "" {
			return fmt.Errorf("memstore: conversation id %q is empty or already used", next.ID)
		}
		if err := ctl.Reset(&e.conv, e.att(attachmentID), attachmentID != "", next.ID, now); err != nil {
			return err
		}
		next.Agent = e.conv.Agent
		ne := &entry{conv: ctl.New(next), atts: map[string]chat.Attachment{}}
		m.convs[next.ID], m.current[next.Agent] = ne, next.ID
		out = snapshot(ne.conv)
		return nil
	})
	return out, err
}

// SetRunnerRef implements chat.Control.
func (m *Control) SetRunnerRef(ctx context.Context, id, ref string) error {
	return m.do(ctx, id, func(e *entry) error {
		if err := ctl.Open(&e.conv); err != nil {
			return err
		}
		e.conv.RunnerRef = ref
		return nil
	})
}

// Bump implements chat.Bumper.
func (m *Control) Bump(ctx context.Context, id string) (chat.Conversation, error) {
	var out chat.Conversation
	err := m.do(ctx, id, func(e *entry) error {
		if err := ctl.Bump(&e.conv); err != nil {
			return err
		}
		out = snapshot(e.conv)
		return nil
	})
	return out, err
}

// Attach implements chat.Control.
func (m *Control) Attach(ctx context.Context, a chat.Attachment) (chat.Attachment, error) {
	err := m.do(ctx, a.ConversationID, func(e *entry) error {
		if err := ctl.Open(&e.conv); err != nil {
			return err
		}
		if _, dup := e.atts[a.ID]; dup || a.ID == "" {
			return fmt.Errorf("memstore: attachment id %q is empty or already used", a.ID)
		}
		a.Generation = e.conv.Generation
		e.atts[a.ID] = a
		return nil
	})
	return a, err
}

// Attachment implements chat.Control.
func (m *Control) Attachment(ctx context.Context, id, attachmentID string) (chat.Attachment, error) {
	var out chat.Attachment
	err := m.do(ctx, id, func(e *entry) error {
		a := e.att(attachmentID)
		if err := ctl.Current(&e.conv, a); err != nil {
			return err
		}
		out = *a
		return nil
	})
	return out, err
}

// Detach implements chat.Control.
func (m *Control) Detach(ctx context.Context, id, attachmentID string, now time.Time) (bool, error) {
	var released bool
	err := m.do(ctx, id, func(e *entry) error {
		var err error
		released, err = ctl.Detach(&e.conv, e.att(attachmentID), now)
		if err == nil {
			delete(e.atts, attachmentID)
		}
		return err
	})
	return released, err
}

// Acquire implements chat.Control.
func (m *Control) Acquire(ctx context.Context, id, attachmentID string, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	var out chat.LeaseChange
	err := m.do(ctx, id, func(e *entry) error {
		var err error
		out, err = ctl.Acquire(&e.conv, e.att(attachmentID), now, ttl)
		return err
	})
	return out, err
}

// Takeover implements chat.Control.
func (m *Control) Takeover(ctx context.Context, id, attachmentID string, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	var out chat.LeaseChange
	err := m.do(ctx, id, func(e *entry) error {
		var err error
		out, err = ctl.Takeover(&e.conv, e.att(attachmentID), now, ttl)
		return err
	})
	return out, err
}

// Holds implements chat.Control.
func (m *Control) Holds(ctx context.Context, id, attachmentID string, now time.Time) error {
	return m.do(ctx, id, func(e *entry) error {
		return ctl.Holds(&e.conv, e.att(attachmentID), now)
	})
}

// BeginRun implements chat.Control.
func (m *Control) BeginRun(ctx context.Context, id, runID, attachmentID string, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	var out chat.LeaseChange
	err := m.do(ctx, id, func(e *entry) error {
		var err error
		out, err = ctl.BeginRun(&e.conv, e.att(attachmentID), attachmentID == "", runID, now, ttl)
		return err
	})
	return out, err
}

// EndRun implements chat.Control.
func (m *Control) EndRun(ctx context.Context, id, runID string) error {
	return m.do(ctx, id, func(e *entry) error {
		ctl.EndRun(&e.conv, runID)
		return nil
	})
}
