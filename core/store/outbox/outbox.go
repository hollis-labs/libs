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
// Leases and tokens. Every Claim writes a random token with the lease, and Ack
// and Fail must present it while the lease is still running; a worker whose
// lease expired (and whose message another worker may now hold) gets
// [ErrLeaseLost] instead of clearing the new owner's claim. Drain skips a
// message whose lease ran out before its turn and gives the handler a context
// that ends with the lease. Keep batch size x worst-case handler time below
// Config.Lease, and note that lease expiry is judged by each worker's own clock:
// drainers on different hosts must have clocks within a small fraction of the
// lease.
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
	"unicode/utf8"

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

// ErrLeaseLost is returned by Ack and Fail when the message is no longer leased
// to the caller: the lease expired, or another worker claimed it since. The
// message will be (or has been) delivered again.
var ErrLeaseLost = errors.New("outbox: lease lost")

// Message is one outbox row, as claimed.
type Message struct {
	ID        string
	Topic     string
	Payload   []byte
	CreatedAt time.Time
	Attempts  int
	LastError string
	// ClaimedUntil is when the lease this claim holds ends.
	ClaimedUntil time.Time

	worker string
	token  string
	// epoch counts Retry calls; a refund is only valid within the epoch it
	// was charged in.
	epoch int64
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
	if err := store.CheckIdentifier(cfg.Table); err != nil || strings.Contains(cfg.Table, ".") {
		return nil, fmt.Errorf("outbox: table %q must be a plain, unqualified identifier", cfg.Table)
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
    claim_token   TEXT,
    claimed_until %s,
    attempts      BIGINT NOT NULL DEFAULT 0,
    epoch         BIGINT NOT NULL DEFAULT 0,
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
	if payload == nil {
		payload = []byte{}
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
// delivery attempt on each. It runs as one write transaction. Worker is a label
// for humans (it is stored in claimed_by); ownership is proven by a per-claim
// token, so two workers with the same name do not clobber each other.
func (o *Outbox) Claim(ctx context.Context, s *store.Store, worker string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 10
	}
	now := o.cfg.Now()
	until := store.Canonical(now.Add(o.cfg.Lease))
	var tok [16]byte
	_, _ = rand.Read(tok[:])
	token := hex.EncodeToString(tok[:])
	var out []Message
	err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		out = out[:0]
		rows, err := tx.QueryContext(ctx, o.ClaimSQL(tx.Dialect()),
			worker, token, until, now, now, o.cfg.MaxAttempts, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Message
			var ls sql.NullString
			if err := rows.Scan(&m.ID, &m.Topic, &m.Payload, store.ScanTime(&m.CreatedAt), &m.Attempts, &ls, &m.epoch); err != nil {
				return err
			}
			m.LastError = ls.String
			m.ClaimedUntil, m.worker, m.token = until, worker, token
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
// claim token, lease expiry, now, now, max attempts, limit.
func (o *Outbox) ClaimSQL(d store.Dialect) string {
	lock := d.ClaimLock()
	return strings.TrimSpace(`UPDATE ` + o.table + `
SET claimed_by = ?, claim_token = ?, claimed_until = ?, attempts = attempts + 1
WHERE id IN (
    SELECT id FROM ` + o.table + `
    WHERE delivered_at IS NULL
      AND available_at <= ?
      AND (claimed_until IS NULL OR claimed_until <= ?)
      AND attempts < ?
    ORDER BY created_at ASC, id ASC
    LIMIT ? ` + lock + `
)
RETURNING id, topic, payload, created_at, attempts, last_error, epoch`)
}

// Ack marks a claimed message delivered. It returns [ErrLeaseLost] when m's
// lease has ended or the message was claimed again.
func (o *Outbox) Ack(ctx context.Context, s *store.Store, m Message) error {
	return o.release(ctx, s, m, `UPDATE `+o.table+` SET delivered_at = ?, claimed_by = NULL, claim_token = NULL, claimed_until = NULL WHERE id = ? AND claim_token = ? AND claimed_until > ?`,
		o.cfg.Now())
}

// Fail releases a claimed message for a later retry after the configured
// backoff, recording the error text (cut to 500 bytes on a character boundary,
// with invalid UTF-8 and NUL removed). It returns [ErrLeaseLost] like Ack.
func (o *Outbox) Fail(ctx context.Context, s *store.Store, m Message, cause error) error {
	msg := ""
	if cause != nil {
		msg = cleanError(cause.Error(), 500)
	}
	retryAt := o.cfg.Now().Add(o.cfg.Backoff(m.Attempts))
	return o.release(ctx, s, m, `UPDATE `+o.table+` SET claimed_by = NULL, claim_token = NULL, claimed_until = NULL, available_at = ?, last_error = ? WHERE id = ? AND claim_token = ? AND claimed_until > ?`,
		retryAt, msg)
}

func (o *Outbox) release(ctx context.Context, s *store.Store, m Message, query string, lead ...any) error {
	if m.token == "" {
		return errors.New("outbox: message was not claimed through Claim or Drain")
	}
	args := append(lead, m.ID, m.token, o.cfg.Now())
	return s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrLeaseLost
		}
		return nil
	})
}

// cleanError makes err text safe for both engines: valid UTF-8, no NUL, at most
// max bytes without cutting a character.
func cleanError(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > max {
		s = s[:max]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// Handler delivers one message. Returning an error schedules a retry.
type Handler func(ctx context.Context, m Message) error

// Drain claims one batch for worker and delivers it, acknowledging each success
// and failing each error. A panic in the handler is contained and treated as a
// failed delivery; its text never reaches last_error (only a reference does).
// A message whose lease ended before its turn is skipped (it will be claimed
// again), and the handler's context ends when its lease does. It returns the
// number delivered and acknowledged.
func (o *Outbox) Drain(ctx context.Context, s *store.Store, worker string, batch int, h Handler) (int, error) {
	msgs, err := o.Claim(ctx, s, worker, batch)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, m := range msgs {
		remaining := m.ClaimedUntil.Sub(o.cfg.Now())
		if remaining <= 0 {
			// Its handler never ran, so the attempt charged at claim is given
			// back; otherwise a slow handler would park the rest of its batch.
			if err := o.giveBack(ctx, s, m); err != nil {
				return done, err
			}
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, remaining)
		herr := o.cfg.Guard.Call("outbox.handler "+m.Topic, func() error { return h(hctx, m) })
		cancel()
		if herr != nil {
			if ferr := o.Fail(ctx, s, m, herr); ferr != nil && !errors.Is(ferr, ErrLeaseLost) {
				return done, ferr
			}
			continue
		}
		if aerr := o.Ack(ctx, s, m); aerr != nil {
			if errors.Is(aerr, ErrLeaseLost) {
				continue
			}
			return done, aerr
		}
		done++
	}
	return done, nil
}

// giveBack refunds the attempt charged to m at claim and releases the claim if
// it is still ours. The refund applies only within m's epoch: a Retry in between
// reset the count, and decrementing it would undercount a later real delivery. The two are separate because another drainer may have
// re-claimed the message after our lease ended: its claim must stand, but our
// charge was for a delivery that never started and has to go regardless.
func (o *Outbox) giveBack(ctx context.Context, s *store.Store, m Message) error {
	return s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+o.table+` SET claimed_by = NULL, claim_token = NULL, claimed_until = NULL WHERE id = ? AND claim_token = ?`,
			m.ID, m.token); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE `+o.table+` SET attempts = attempts - 1 WHERE id = ? AND epoch = ? AND attempts > 0 AND delivered_at IS NULL`,
			m.ID, m.epoch)
		return err
	})
}

// Parked lists messages that failed MaxAttempts deliveries and are no longer
// claimed, oldest first, up to limit (default 100).
func (o *Outbox) Parked(ctx context.Context, q store.Querier, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := q.QueryContext(ctx, `SELECT id, topic, payload, created_at, attempts, last_error FROM `+o.table+
		` WHERE delivered_at IS NULL AND attempts >= ? ORDER BY created_at ASC, id ASC LIMIT ?`, o.cfg.MaxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Topic, &m.Payload, store.ScanTime(&m.CreatedAt), &m.Attempts, &m.LastError); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Retry puts a parked (or waiting) undelivered message back in line: attempts
// reset, available now, claim cleared. It reports whether a message was changed.
func (o *Outbox) Retry(ctx context.Context, s *store.Store, id string) (bool, error) {
	var n int64
	err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE `+o.table+` SET attempts = 0, epoch = epoch + 1, available_at = ?, claimed_by = NULL, claim_token = NULL, claimed_until = NULL WHERE id = ? AND delivered_at IS NULL`,
			o.cfg.Now(), id)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n > 0, err
}

// Pending counts undelivered messages that will still be tried; parked ones
// (see [Outbox.Parked]) are not included.
func (o *Outbox) Pending(ctx context.Context, q store.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+o.table+` WHERE delivered_at IS NULL AND attempts < ?`, o.cfg.MaxAttempts).Scan(&n)
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
