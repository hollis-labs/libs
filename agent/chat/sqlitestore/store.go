// Package sqlitestore keeps a conversation's event log and its control
// records in one SQLite database, on core/store.
//
// A [Store] is both a streamhub.Log (it passes streamhub's hubtest log
// conformance suite) and the built-in chat.Control (it passes
// chattest.ControlConformance): conversations, attachments, the input lease
// and the events all live in the same file.
//
//	st, err := sqlitestore.Open(ctx, "chat.db")
//	svc, err := chat.NewService(chat.Config{Log: st, Control: st, ...})
//
// Every event is stored, deltas included, so a client that reconnects in the
// middle of a message resumes without loss. Nothing prunes the log; Trim is
// there for a host that decides to.
//
// One process owns the database. Writes go through core/store's single writer
// connection.
package sqlitestore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

//go:embed migrations
var migrations embed.FS

// source is this package's schema. It has its own version table, so the
// tables can share a database with a host's own.
var source = migrate.Source{FS: migrations, Root: "migrations", Table: "chat_schema_version"}

const defaultPageSize = 256

// Option configures a [Store].
type Option func(*options)

type options struct {
	now         func() time.Time
	page        int
	synchronous string
}

// WithClock sets the clock that stamps Record.At and measures Trim's MaxAge.
func WithClock(now func() time.Time) Option {
	return func(o *options) {
		if now != nil {
			o.now = now
		}
	}
}

// WithPageSize sets how many records After reads per query (default 256).
func WithPageSize(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.page = n
		}
	}
}

// WithSynchronous sets SQLite's synchronous pragma for a database [Open]
// opens: OFF, NORMAL, FULL or EXTRA. The default is NORMAL: every streamed
// delta is its own commit, and in WAL mode NORMAL keeps each commit safe
// across a crash of the process without an fsync per token. A power loss can
// cost the last moments of a stream, never the database. Use FULL when that is
// not acceptable. [New] ignores it: the host opened that store.
func WithSynchronous(mode string) Option {
	return func(o *options) { o.synchronous = mode }
}

func resolve(opts []Option) options {
	o := options{now: time.Now, page: defaultPageSize, synchronous: "NORMAL"}
	for _, f := range opts {
		f(&o)
	}
	return o
}

// Store is a streamhub.Log and a chat.Control on one SQLite database. It is
// safe for concurrent use.
type Store struct {
	db    *store.Store
	owned bool
	now   func() time.Time
	page  int
	epoch string

	mu     sync.RWMutex // closed versus calls in flight
	closed bool
}

var (
	_ streamhub.Log         = (*Store)(nil)
	_ streamhub.EpochReader = (*Store)(nil)
	_ chat.Control          = (*Store)(nil)
	_ chat.Bumper           = (*Store)(nil)
)

// Open opens or creates the database at path (a file path, or ":memory:") and
// applies the schema. Close closes the database.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	db, err := store.Open(ctx, store.Config{
		Driver: store.DriverSQLite,
		SQLite: store.SQLiteConfig{Path: path, CreateParentDir: true, Synchronous: resolve(opts).synchronous},
	})
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: open: %w", err)
	}
	s, err := New(ctx, db, opts...)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s.owned = true
	return s, nil
}

// New applies the schema to a SQLite store the host already has open and
// returns a Store over it. Close then leaves db open.
func New(ctx context.Context, db *store.Store, opts ...Option) (*Store, error) {
	if db == nil || db.Dialect().Name() != store.DriverSQLite {
		return nil, errors.New("sqlitestore: New needs an open SQLite store")
	}
	if err := migrate.UpStore(ctx, db, source); err != nil {
		return nil, fmt.Errorf("sqlitestore: migrate: %w", err)
	}
	o := resolve(opts)
	s := &Store{db: db, now: o.now, page: o.page}
	// The epoch names this log: minted once per database, kept across reopens.
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read does not fail
	err := db.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chat_meta(key, value) VALUES('epoch', ?) ON CONFLICT(key) DO NOTHING`, hex.EncodeToString(b[:])); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT value FROM chat_meta WHERE key = 'epoch'`).Scan(&s.epoch)
	})
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: read epoch: %w", err)
	}
	return s, nil
}

// Close implements streamhub.Log. Later calls return streamhub.ErrClosed. It
// closes the database when the Store opened it.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if !s.owned {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("sqlitestore: close: %w", err)
	}
	return nil
}

// enter admits a call unless ctx has ended or the Store is closed. The caller
// must call leave.
func (s *Store) enter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return streamhub.ErrClosed
	}
	return nil
}

func (s *Store) leave() { s.mu.RUnlock() }

func noRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
