package sqlitestore

import (
	"context"
	"errors"
	"fmt"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/internal/ctl"
	"github.com/hollis-labs/libs/core/store"
)

const convColumns = `id, agent, scope, owner, kind, generation, runner_ref, active_run,
	lease_holder, lease_expires_ns, closed, successor, created_ns`

func loadConv(ctx context.Context, q store.Querier, where string, arg any) (chat.Conversation, error) {
	var (
		c               chat.Conversation
		holder          string
		expires, create int64
	)
	err := q.QueryRowContext(ctx, `SELECT `+convColumns+` FROM chat_conversations WHERE `+where, arg).Scan( //nolint:gosec // where is one of two constant clauses
		&c.ID, &c.Agent, &c.Scope, &c.Owner, &c.Kind, &c.Generation, &c.RunnerRef, &c.ActiveRun,
		&holder, &expires, &c.Closed, &c.Successor, &create)
	if noRows(err) {
		return c, chat.ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt = time.Unix(0, create).UTC()
	if holder != "" {
		c.Lease = &chat.Lease{Holder: holder, ExpiresAt: time.Unix(0, expires).UTC()}
	}
	return c, nil
}

func insertConv(ctx context.Context, tx store.Tx, c chat.Conversation) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO chat_conversations(id, agent, scope, owner, kind, generation, created_ns) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Agent, c.Scope, c.Owner, c.Kind, c.Generation, c.CreatedAt.UnixNano())
	return err
}

// saveConv writes back everything the lease rules can change.
func saveConv(ctx context.Context, tx store.Tx, c chat.Conversation) error {
	var (
		holder  string
		expires int64
	)
	if c.Lease != nil {
		holder, expires = c.Lease.Holder, c.Lease.ExpiresAt.UnixNano()
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE chat_conversations SET generation = ?, runner_ref = ?, active_run = ?, lease_holder = ?,
			lease_expires_ns = ?, closed = ?, successor = ? WHERE id = ?`,
		c.Generation, c.RunnerRef, c.ActiveRun, holder, expires, c.Closed, c.Successor, c.ID)
	return err
}

func loadAtt(ctx context.Context, q store.Querier, conversationID, id string) (*chat.Attachment, error) {
	if id == "" {
		return nil, nil
	}
	a := chat.Attachment{ID: id, ConversationID: conversationID}
	var created int64
	err := q.QueryRowContext(ctx,
		`SELECT role, stream, generation, created_ns FROM chat_attachments WHERE conversation_id = ? AND id = ?`,
		conversationID, id).Scan(&a.Role, &a.Stream, &a.Generation, &created)
	if noRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.CreatedAt = time.Unix(0, created).UTC()
	return &a, nil
}

// change loads a conversation and the named attachment in a write
// transaction, applies f and writes the conversation back when f succeeds.
func (s *Store) change(ctx context.Context, what, conversationID, attachmentID string,
	f func(ctx context.Context, tx store.Tx, c *chat.Conversation, a *chat.Attachment) error) error {
	if err := s.enter(ctx); err != nil {
		return err
	}
	defer s.leave()
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		c, err := loadConv(ctx, tx, `id = ?`, conversationID)
		if err != nil {
			return err
		}
		a, err := loadAtt(ctx, tx, conversationID, attachmentID)
		if err != nil {
			return err
		}
		if err := f(ctx, tx, &c, a); err != nil {
			return err
		}
		return saveConv(ctx, tx, c)
	})
	if err != nil {
		return fmt.Errorf("sqlitestore: %s %q: %w", what, conversationID, err)
	}
	return nil
}

// Ensure implements chat.Control.
func (s *Store) Ensure(ctx context.Context, nc chat.NewConversation) (chat.Conversation, bool, error) {
	if err := s.enter(ctx); err != nil {
		return chat.Conversation{}, false, err
	}
	defer s.leave()
	var (
		out     chat.Conversation
		created bool
	)
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		created = false
		cur, err := loadConv(ctx, tx, `agent = ? AND closed = 0`, nc.Agent)
		if err == nil {
			out = cur
			return nil
		}
		if !errors.Is(err, chat.ErrNotFound) {
			return err
		}
		out, created = ctl.New(nc), true
		return insertConv(ctx, tx, out)
	})
	if err != nil {
		return chat.Conversation{}, false, fmt.Errorf("sqlitestore: ensure conversation for %q: %w", nc.Agent, err)
	}
	return out, created, nil
}

// Get implements chat.Control.
func (s *Store) Get(ctx context.Context, conversationID string) (chat.Conversation, error) {
	if err := s.enter(ctx); err != nil {
		return chat.Conversation{}, err
	}
	defer s.leave()
	c, err := loadConv(ctx, s.db.Reader(), `id = ?`, conversationID)
	if err != nil {
		return chat.Conversation{}, fmt.Errorf("sqlitestore: conversation %q: %w", conversationID, err)
	}
	return c, nil
}

// Reset implements chat.Control.
func (s *Store) Reset(ctx context.Context, conversationID, attachmentID string, next chat.NewConversation, now time.Time) (chat.Conversation, error) {
	var out chat.Conversation
	err := s.change(ctx, "reset", conversationID, attachmentID, func(ctx context.Context, tx store.Tx, c *chat.Conversation, a *chat.Attachment) error {
		if err := ctl.Reset(c, a, attachmentID != "", next.ID, now); err != nil {
			return err
		}
		// The old row must stop being the agent's current one before the new
		// row can take its place in the unique index.
		if err := saveConv(ctx, tx, *c); err != nil {
			return err
		}
		next.Agent = c.Agent
		out = ctl.New(next)
		return insertConv(ctx, tx, out)
	})
	return out, err
}

// SetRunnerRef implements chat.Control.
func (s *Store) SetRunnerRef(ctx context.Context, conversationID, ref string) error {
	return s.change(ctx, "set runner ref", conversationID, "", func(_ context.Context, _ store.Tx, c *chat.Conversation, _ *chat.Attachment) error {
		if err := ctl.Open(c); err != nil {
			return err
		}
		c.RunnerRef = ref
		return nil
	})
}

// Bump implements chat.Bumper.
func (s *Store) Bump(ctx context.Context, conversationID string) (chat.Conversation, error) {
	var out chat.Conversation
	err := s.change(ctx, "bump", conversationID, "", func(_ context.Context, _ store.Tx, c *chat.Conversation, _ *chat.Attachment) error {
		if err := ctl.Bump(c); err != nil {
			return err
		}
		out = *c
		return nil
	})
	return out, err
}

// Attach implements chat.Control.
func (s *Store) Attach(ctx context.Context, a chat.Attachment) (chat.Attachment, error) {
	err := s.change(ctx, "attach", a.ConversationID, "", func(ctx context.Context, tx store.Tx, c *chat.Conversation, _ *chat.Attachment) error {
		if err := ctl.Open(c); err != nil {
			return err
		}
		if a.ID == "" {
			return errors.New("attachment id is empty")
		}
		a.Generation = c.Generation
		_, err := tx.ExecContext(ctx,
			`INSERT INTO chat_attachments(conversation_id, id, role, stream, generation, created_ns) VALUES(?, ?, ?, ?, ?, ?)`,
			a.ConversationID, a.ID, string(a.Role), string(a.Stream), a.Generation, a.CreatedAt.UnixNano())
		return err
	})
	return a, err
}

// Attachment implements chat.Control.
func (s *Store) Attachment(ctx context.Context, conversationID, attachmentID string) (chat.Attachment, error) {
	if err := s.enter(ctx); err != nil {
		return chat.Attachment{}, err
	}
	defer s.leave()
	var out chat.Attachment
	err := s.db.ReadTx(ctx, func(ctx context.Context, q store.Querier) error {
		c, err := loadConv(ctx, q, `id = ?`, conversationID)
		if err != nil {
			return err
		}
		a, err := loadAtt(ctx, q, conversationID, attachmentID)
		if err != nil {
			return err
		}
		if err := ctl.Current(&c, a); err != nil {
			return err
		}
		out = *a
		return nil
	})
	if err != nil {
		return chat.Attachment{}, fmt.Errorf("sqlitestore: attachment %q: %w", attachmentID, err)
	}
	return out, nil
}

// Detach implements chat.Control.
func (s *Store) Detach(ctx context.Context, conversationID, attachmentID string, now time.Time) (bool, error) {
	var released bool
	err := s.change(ctx, "detach", conversationID, attachmentID, func(ctx context.Context, tx store.Tx, c *chat.Conversation, a *chat.Attachment) error {
		var err error
		if released, err = ctl.Detach(c, a, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM chat_attachments WHERE conversation_id = ? AND id = ?`, conversationID, attachmentID)
		return err
	})
	return released, err
}

// Acquire implements chat.Control.
func (s *Store) Acquire(ctx context.Context, conversationID, attachmentID string, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	var out chat.LeaseChange
	err := s.change(ctx, "acquire lease", conversationID, attachmentID, func(_ context.Context, _ store.Tx, c *chat.Conversation, a *chat.Attachment) error {
		var err error
		out, err = ctl.Acquire(c, a, now, ttl)
		return err
	})
	return out, err
}

// Takeover implements chat.Control.
func (s *Store) Takeover(ctx context.Context, conversationID, attachmentID string, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	var out chat.LeaseChange
	err := s.change(ctx, "take over lease", conversationID, attachmentID, func(_ context.Context, _ store.Tx, c *chat.Conversation, a *chat.Attachment) error {
		var err error
		out, err = ctl.Takeover(c, a, now, ttl)
		return err
	})
	return out, err
}

// Holds implements chat.Control.
func (s *Store) Holds(ctx context.Context, conversationID, attachmentID string, now time.Time) error {
	if err := s.enter(ctx); err != nil {
		return err
	}
	defer s.leave()
	err := s.db.ReadTx(ctx, func(ctx context.Context, q store.Querier) error {
		c, err := loadConv(ctx, q, `id = ?`, conversationID)
		if err != nil {
			return err
		}
		a, err := loadAtt(ctx, q, conversationID, attachmentID)
		if err != nil {
			return err
		}
		return ctl.Holds(&c, a, now)
	})
	if err != nil {
		return fmt.Errorf("sqlitestore: lease check %q: %w", conversationID, err)
	}
	return nil
}

// BeginRun implements chat.Control.
func (s *Store) BeginRun(ctx context.Context, conversationID, runID, attachmentID string, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	var out chat.LeaseChange
	err := s.change(ctx, "begin run", conversationID, attachmentID, func(_ context.Context, _ store.Tx, c *chat.Conversation, a *chat.Attachment) error {
		var err error
		out, err = ctl.BeginRun(c, a, attachmentID == "", runID, now, ttl)
		return err
	})
	return out, err
}

// EndRun implements chat.Control.
func (s *Store) EndRun(ctx context.Context, conversationID, runID string) error {
	return s.change(ctx, "end run", conversationID, "", func(_ context.Context, _ store.Tx, c *chat.Conversation, _ *chat.Attachment) error {
		ctl.EndRun(c, runID)
		return nil
	})
}
