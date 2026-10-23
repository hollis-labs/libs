// Package outbox is a transactional outbox: a message is written in the same
// transaction as the business change that produced it, and a drainer later
// claims it, delivers it and acknowledges it. Delivery is at least once.
//
// The claim statement is per dialect: on Postgres it takes rows with FOR UPDATE
// SKIP LOCKED so several drainers can run side by side; on SQLite writers are
// already serialised by the IMMEDIATE transaction, so the same statement needs
// no row lock. A claim is a lease: a drainer that dies leaves messages that
// become claimable again when the lease expires.
//
// This package is the storage half. It does not replace core/util/queue (which
// has no Postgres driver); a queue driver over this table is separate work.
package outbox

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/libs/core/panics"
	"github.com/hollis-labs/libs/core/store"
)

// DefaultTable is the outbox table unless Config.Table says otherwise.
const DefaultTable = "outbox_messages"

// Config tunes an Outbox.
type Config struct {
	// Table name. Default outbox_messages. Validated as an identifier.
	Table string
	// Lease is how long a claim holds a message. Default 60s.
	Lease time.Duration
	// MaxAttempts: a message that has failed this many deliveries is parked
	// (never claimed again until reset). Default 10; zero keeps the default.
	MaxAttempts int
	// Backoff is the delay before a failed message is retried, from the number
	// of attempts so far. Default: 1s doubling, capped at 5 minutes.
	Backoff func(attempts int) time.Duration
	// Now overrides the clock, for tests.
	Now func() time.Time
	// Guard contains panics in a delivery handler. Nil uses panics.Default().
	Guard interface {
		Call(where string, fn func() error, opts ...panics.Option) error
	}
}

// Message is one outbox row.
type Message struct {
	ID        string
	Topic     string
	Payload   []byte
	CreatedAt time.Time
	Attempts  int
	LastError string
}

// Outbox is the helper bound to a table and a dialect.
type Outbox struct {
	cfg   Config
	table string
}

// New validates cfg and returns an Outbox.
func New(cfg Config) (*Outbox, error) {
	if cfg.Table == "" {
		cfg.Table = DefaultTable
	}
	if err := store.CheckIdentifier(cfg.Table); err != nil {
		return nil, err
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 60 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 10
	}
	if cfg.Backoff == nil {
		cfg.Backoff = func(n int) time.Duration {
			d := time.Second << min(max(n-1, 0), 9)
			return min(d, 5*time.Minute)
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Guard == nil {
		cfg.Guard = panics.Default()
	}
	return &Outbox{cfg: cfg, table: cfg.Table}, nil
}

// DDL returns the CREATE statements for the outbox table in a dialect, for a
// migration file. Timestamps are TEXT (fixed-width UTC) on SQLite and
// TIMESTAMPTZ on Postgres.
func (o *Outbox) DDL(d store.Dialect) []string {
	ts, payload := "TEXT", "TEXT"
	if d.Name() == store.DriverPostgres {
		ts, payload = "TIMESTAMPTZ", "BYTEA"
	} else {
		payload = "BLOB"
	}
	t := o.table
	return []string{
		fmt.Sprintf(`CREATE TABLE %s (
    id            TEXT PRIMARY KEY,
    topic         TEXT NOT NULL,
    payload       %s NOT NULL,
    created_at    %s NOT NULL,
    available_at  %s NOT NULL,
    claimed_by    TEXT,
    claimed_until %s,
    attempts      BIGINT NOT NULL DEFAULT 0,
    last_error    TEXT NOT NULL DEFAULT '',
    delivered_at  %s
)`, t, payload, ts, ts, ts, ts),
		fmt.Sprintf(`CREATE INDEX %s_pending ON %s (available_at, created_at) WHERE delivered_at IS NULL`, t, t),
	}
}

// NewID returns a time-ordered unique message id.
func NewID(now time.Time) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%016x%s", now.UTC().UnixNano(), hex.EncodeToString(b[:]))
}

// Enqueue writes a message through q, which must be the Tx of the transaction
// that makes the business change. A zero AvailableAt delivers as soon as
// possible. It returns the message id.
func (o *Outbox) Enqueue(ctx context.Context, q store.Querier, topic string, payload []byte, opts ...EnqueueOption) (string, error) {
	if topic == "" {
		return "", errors.New("outbox: empty topic")
	}
	e := enqueueOpts{id: "", at: time.Time{}}
	for _, f := range opts {
		f(&e)
	}
	now := o.cfg.Now()
	if e.id == "" {
		e.id = NewID(now)
	}
	if e.at.IsZero() {
		e.at = now
	}
	_, err := q.ExecContext(ctx,
		`INSERT INTO `+o.table+` (id, topic, payload, created_at, available_at) VALUES (?, ?, ?, ?, ?)`,
		e.id, topic, payload, now, e.at)
	if err != nil {
		return "", fmt.Errorf("outbox: enqueue: %w", err)
	}
	return e.id, nil
}

type enqueueOpts struct {
	id string
	at time.Time
}

// EnqueueOption adjusts one Enqueue.
type EnqueueOption func(*enqueueOpts)

// WithID sets the message id (idempotent enqueue: a duplicate id fails with a
// unique violation).
func WithID(id string) EnqueueOption { return func(e *enqueueOpts) { e.id = id } }

// WithAvailableAt delays delivery until t.
func WithAvailableAt(t time.Time) EnqueueOption { return func(e *enqueueOpts) { e.at = t } }

// Claim leases up to limit ready messages to worker, oldest first, and counts a
// delivery attempt on each. It runs as one write transaction.
func (o *Outbox) Claim(ctx context.Context, s *store.Store, worker string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 10
	}
	now := o.cfg.Now()
	var out []Message
	err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		out = out[:0]
		rows, err := tx.QueryContext(ctx, o.ClaimSQL(tx.Dialect()),
			worker, now.Add(o.cfg.Lease), now, now, o.cfg.MaxAttempts, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Message
			var ls sql.NullString
			if err := rows.Scan(&m.ID, &m.Topic, &m.Payload, store.ScanTime(&m.CreatedAt), &m.Attempts, &ls); err != nil {
				return err
			}
			m.LastError = ls.String
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: claim: %w", err)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ClaimSQL is the claim statement for a dialect. Arguments, in order: worker,
// lease expiry, now, now, max attempts, limit.
func (o *Outbox) ClaimSQL(d store.Dialect) string {
	lock := d.ClaimLock()
	return strings.TrimSpace(`UPDATE ` + o.table + `
SET claimed_by = ?, claimed_until = ?, attempts = attempts + 1
WHERE id IN (
    SELECT id FROM ` + o.table + `
    WHERE delivered_at IS NULL
      AND available_at <= ?
      AND (claimed_until IS NULL OR claimed_until <= ?)
      AND attempts < ?
    ORDER BY created_at ASC, id ASC
    LIMIT ? ` + lock + `
)
RETURNING id, topic, payload, created_at, attempts, last_error`)
}

// Ack marks a claimed message delivered. It is a no-op when the lease was lost
// to another worker.
func (o *Outbox) Ack(ctx context.Context, s *store.Store, worker, id string) error {
	return s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE `+o.table+` SET delivered_at = ?, claimed_by = NULL, claimed_until = NULL WHERE id = ? AND claimed_by = ?`,
			o.cfg.Now(), id, worker)
		return err
	})
}

// Fail releases a claimed message for a later retry, recording the error text.
func (o *Outbox) Fail(ctx context.Context, s *store.Store, worker, id string, cause error, attempts int) error {
	retryAt := o.cfg.Now().Add(o.cfg.Backoff(attempts))
	msg := ""
	if cause != nil {
		msg = cause.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
	}
	return s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE `+o.table+` SET claimed_by = NULL, claimed_until = NULL, available_at = ?, last_error = ? WHERE id = ? AND claimed_by = ?`,
			retryAt, msg, id, worker)
		return err
	})
}

// Handler delivers one message. Returning an error schedules a retry.
type Handler func(ctx context.Context, m Message) error

// Drain claims one batch for worker and delivers it, acknowledging each success
// and failing each error. A panic in the handler is contained and treated as a
// failed delivery; its text never reaches last_error (only a reference does). It
// returns the number delivered.
func (o *Outbox) Drain(ctx context.Context, s *store.Store, worker string, batch int, h Handler) (int, error) {
	msgs, err := o.Claim(ctx, s, worker, batch)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, m := range msgs {
		herr := o.cfg.Guard.Call("outbox.handler "+m.Topic, func() error { return h(ctx, m) })
		if herr != nil {
			if ferr := o.Fail(ctx, s, worker, m.ID, herr, m.Attempts); ferr != nil {
				return done, ferr
			}
			continue
		}
		if aerr := o.Ack(ctx, s, worker, m.ID); aerr != nil {
			return done, aerr
		}
		done++
	}
	return done, nil
}

// Pending counts undelivered messages, parked ones included.
func (o *Outbox) Pending(ctx context.Context, q store.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+o.table+` WHERE delivered_at IS NULL`).Scan(&n)
	return n, err
}

// Purge deletes messages delivered before t.
func (o *Outbox) Purge(ctx context.Context, s *store.Store, before time.Time) (int64, error) {
	var n int64
	err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM `+o.table+` WHERE delivered_at IS NOT NULL AND delivered_at < ?`, before)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}
