package delivery

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// TransitionReason identifies the operation that changed an obligation's
// status. It contains no caller-supplied error text, payload, or lease secret;
// diagnostic details remain on the attempt and receipt records.
type TransitionReason string

const (
	TransitionCreated          TransitionReason = "created"
	TransitionClaimed          TransitionReason = "claimed"
	TransitionConsumed         TransitionReason = "consumed"
	TransitionNacked           TransitionReason = "nacked"
	TransitionRedriven         TransitionReason = "redriven"
	TransitionLeaseExpired     TransitionReason = "lease_expired"
	TransitionDeadlineExceeded TransitionReason = "deadline_exceeded"
)

// Transition is an immutable status change, committed atomically with the
// delivery mutation. ID is a store-wide ascending cursor, not a timestamp;
// readers must not assume IDs are contiguous. From is empty only on creation
// (including legacy import). AttemptID is empty when no attempt caused the
// change. At is the mutation's UpdatedAt; it need not increase with ID.
//
// The log contains no historical reconstruction when upgrading an existing
// store. It is independent of receipts: intermediate handoff stages, lease
// extension, idempotent replay, and other same-status writes add no transition.
// Reads never trigger expiry or other mutations. Retention and publication
// checkpoints belong to the host; reading does not acknowledge or delete rows.
type Transition struct {
	ID         int64            `json:"id"`
	DeliveryID DeliveryID       `json:"delivery_id"`
	MessageID  MessageID        `json:"message_id"`
	AttemptID  AttemptID        `json:"attempt_id,omitempty"`
	From       DeliveryStatus   `json:"from"`
	To         DeliveryStatus   `json:"to"`
	Reason     TransitionReason `json:"reason"`
	At         time.Time        `json:"at"`
}

func nackTransitionReason(req NackRequest, d RecipientDelivery) TransitionReason {
	if deadlineExpired(d.UpdatedAt, d) || (!req.NextAttemptAt.IsZero() && deadlineExpired(req.NextAttemptAt, d)) {
		return TransitionDeadlineExceeded
	}
	return TransitionNacked
}

// Transitions returns at most limit rows in ascending ID order, strictly after
// after (zero starts at the beginning). after must be nonnegative and limit
// positive, otherwise ErrInvalidArgument is returned. The returned slice is a
// snapshot; callers resume with the last ID only after processing that row.
func (s *MemoryStore) Transitions(ctx context.Context, after int64, limit int) ([]Transition, error) {
	if after < 0 || limit <= 0 {
		return nil, ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := sort.Search(len(s.transitions), func(i int) bool { return s.transitions[i].ID > after })
	n := len(s.transitions) - start
	if n > limit {
		n = limit
	}
	out := make([]Transition, n)
	copy(out, s.transitions[start:start+n])
	return out, nil
}

func (s *MemoryStore) saveDeliveryLocked(d RecipientDelivery, attemptID AttemptID, reason TransitionReason) {
	from := s.deliveries[d.ID].Status
	s.deliveries[d.ID] = d
	if from == d.Status {
		return
	}
	var id int64 = 1
	if n := len(s.transitions); n != 0 {
		id = s.transitions[n-1].ID + 1
	}
	s.transitions = append(s.transitions, Transition{ID: id, DeliveryID: d.ID, MessageID: d.MessageID,
		AttemptID: attemptID, From: from, To: d.Status, Reason: reason, At: d.UpdatedAt})
}

// Transitions returns committed rows using the same exclusive cursor and
// positive limit contract as MemoryStore.Transitions. It never changes state.
func (s *SQLiteStore) Transitions(ctx context.Context, after int64, limit int) ([]Transition, error) {
	if after < 0 || limit <= 0 {
		return nil, ErrInvalidArgument
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, delivery_id, message_id, attempt_id, from_status, to_status, reason, at
		FROM messaging_transitions WHERE id > ? ORDER BY id ASC LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Transition{}
	for rows.Next() {
		var tr Transition
		var at string
		if err := rows.Scan(&tr.ID, &tr.DeliveryID, &tr.MessageID, &tr.AttemptID, &tr.From, &tr.To, &tr.Reason, &at); err != nil {
			return nil, err
		}
		tr.At, err = parseTime(at)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// q must be the transaction containing the delivery write. No separate
// observer/callback is allowed to publish a status change after commit.
func sqliteAppendTransition(ctx context.Context, q *sql.Tx, d RecipientDelivery, from DeliveryStatus, attemptID AttemptID, reason TransitionReason) error {
	if from == d.Status {
		return nil
	}
	_, err := q.ExecContext(ctx, `INSERT INTO messaging_transitions
		(delivery_id, message_id, attempt_id, from_status, to_status, reason, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.MessageID, attemptID, from, d.Status, reason, timeString(d.UpdatedAt))
	return err
}
