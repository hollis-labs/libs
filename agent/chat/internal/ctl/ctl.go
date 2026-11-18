// Package ctl holds the lease and generation rules as pure functions over one
// conversation record, so the built-in Control implementations cannot disagree
// about them. A store loads the record (and the caller's attachment, when the
// operation names one), applies a function and writes the record back.
package ctl

import (
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
)

// Open returns a *chat.ClosedError for a conversation that was reset.
func Open(c *chat.Conversation) error {
	if c.Closed {
		return &chat.ClosedError{ConversationID: c.ID, Successor: c.Successor}
	}
	return nil
}

// Current checks that a is a current attachment of c. a is nil when the store
// holds no such attachment.
func Current(c *chat.Conversation, a *chat.Attachment) error {
	if err := Open(c); err != nil {
		return err
	}
	if a == nil {
		return chat.ErrUnknownAttachment
	}
	if a.Generation != c.Generation {
		return &chat.StaleError{Generation: c.Generation}
	}
	return nil
}

func take(c *chat.Conversation, a *chat.Attachment, now time.Time, ttl time.Duration, force bool) (chat.LeaseChange, error) {
	if err := Current(c, a); err != nil {
		return chat.LeaseChange{}, err
	}
	if a.Role != chat.RoleController {
		return chat.LeaseChange{}, chat.ErrNotController
	}
	held, ok := c.LeaseAt(now)
	next := chat.Lease{Holder: a.ID, ExpiresAt: now.Add(ttl)}
	switch {
	case ok && held.Holder == a.ID:
		c.Lease = &next
		return chat.LeaseChange{Lease: next}, nil
	case ok && !force:
		return chat.LeaseChange{}, &chat.LeaseHeldError{Lease: held}
	}
	c.Lease = &next
	return chat.LeaseChange{Lease: next, Acquired: true, Previous: held.Holder}, nil
}

// Acquire gives a the lease or renews it.
func Acquire(c *chat.Conversation, a *chat.Attachment, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	return take(c, a, now, ttl, false)
}

// Takeover gives a the lease whoever holds it.
func Takeover(c *chat.Conversation, a *chat.Attachment, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	return take(c, a, now, ttl, true)
}

// Holds reports whether a holds the unexpired lease.
func Holds(c *chat.Conversation, a *chat.Attachment, now time.Time) error {
	if err := Current(c, a); err != nil {
		return err
	}
	if held, ok := c.LeaseAt(now); !ok || held.Holder != a.ID {
		return chat.ErrLeaseRequired
	}
	return nil
}

// BeginRun records runID as the active run. host is true for a run that needs
// no lease; a is then ignored.
func BeginRun(c *chat.Conversation, a *chat.Attachment, host bool, runID string, now time.Time, ttl time.Duration) (chat.LeaseChange, error) {
	var change chat.LeaseChange
	if host {
		if err := Open(c); err != nil {
			return change, err
		}
	} else {
		if err := Current(c, a); err != nil {
			return change, err
		}
		held, ok := c.LeaseAt(now)
		switch {
		case ok && held.Holder == a.ID:
			change.Lease = held
		case ok || a.Role != chat.RoleController:
			return chat.LeaseChange{}, chat.ErrLeaseRequired
		default:
			change = chat.LeaseChange{Lease: chat.Lease{Holder: a.ID, ExpiresAt: now.Add(ttl)}, Acquired: true}
		}
	}
	if c.ActiveRun != "" {
		return chat.LeaseChange{}, chat.ErrBusy
	}
	if change.Acquired {
		l := change.Lease
		c.Lease = &l
	}
	c.ActiveRun = runID
	return change, nil
}

// EndRun clears the active run if it is runID.
func EndRun(c *chat.Conversation, runID string) {
	if c.ActiveRun == runID {
		c.ActiveRun = ""
	}
}

// Detach checks that a may be removed and releases the lease if it holds it.
func Detach(c *chat.Conversation, a *chat.Attachment, now time.Time) (released bool, err error) {
	if err := Current(c, a); err != nil {
		return false, err
	}
	if c.Lease != nil && c.Lease.Holder == a.ID {
		_, released = c.LeaseAt(now)
		c.Lease = nil
	}
	return released, nil
}

// Bump raises the generation, which makes every attachment stale, and clears
// the lease and the active run.
func Bump(c *chat.Conversation) error {
	if err := Open(c); err != nil {
		return err
	}
	c.Generation++
	c.Lease = nil
	c.ActiveRun = ""
	return nil
}

// Reset checks that c may be reset on behalf of a (nil when the caller named
// no attachment) and marks it closed with the given successor.
func Reset(c *chat.Conversation, a *chat.Attachment, named bool, successor string, now time.Time) error {
	if err := Open(c); err != nil {
		return err
	}
	if named {
		if err := Current(c, a); err != nil {
			return err
		}
	}
	if held, ok := c.LeaseAt(now); ok && (a == nil || held.Holder != a.ID) {
		return chat.ErrLeaseRequired
	}
	if c.ActiveRun != "" {
		return chat.ErrBusy
	}
	c.Closed, c.Successor, c.Lease = true, successor, nil
	return nil
}

// New builds the record for a new conversation.
func New(nc chat.NewConversation) chat.Conversation {
	return chat.Conversation{
		ID: nc.ID, Agent: nc.Agent, Scope: nc.Scope, Owner: nc.Owner, Kind: nc.Kind,
		Generation: 1, CreatedAt: nc.At,
	}
}
