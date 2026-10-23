package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/libs/core/panics"
	"github.com/hollis-labs/libs/core/util/sqlite/sqlitekit"
	"github.com/hollis-labs/libs/core/util/sqlite/txutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// PanicGuard is the part of *panics.Guard the store uses to contain a panic in
// a transaction callback.
type PanicGuard interface {
	Call(where string, fn func() error, opts ...panics.Option) error
}

// Querier is the statement surface shared by [Store] reads, [Tx] and the
// subpackages. "?" placeholders are rebound and time arguments encoded for the
// engine before the statement reaches the driver.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	Dialect() Dialect
}

// Tx is the transaction handle a [Store.WriteTx] callback receives. It is the
// only handle the callback may use; see the package documentation.
type Tx interface{ Querier }

type rawQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// bound adapts a database/sql handle to [Querier].
type bound struct {
	d   Dialect
	raw rawQuerier
	// cache memoises Rebind results; statements are a small fixed set.
	cache *sync.Map
}

func newBound(d Dialect, raw rawQuerier, cache *sync.Map) *bound {
	return &bound{d: d, raw: raw, cache: cache}
}

func (b *bound) Dialect() Dialect { return b.d }

func (b *bound) rebind(q string) string {
	if v, ok := b.cache.Load(q); ok {
		return v.(string)
	}
	r := b.d.Rebind(q)
	b.cache.Store(q, r)
	return r
}

func (b *bound) args(in []any) []any {
	var out []any
	for i, a := range in {
		var enc any
		switch v := a.(type) {
		case time.Time:
			enc = b.d.EncodeTime(v)
		case *time.Time:
			if v == nil {
				enc = nil
			} else {
				enc = b.d.EncodeTime(*v)
			}
		default:
			continue
		}
		if out == nil {
			out = append([]any(nil), in...)
		}
		out[i] = enc
	}
	if out == nil {
		return in
	}
	return out
}

func (b *bound) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return b.raw.ExecContext(ctx, b.rebind(q), b.args(a)...)
}
func (b *bound) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return b.raw.QueryContext(ctx, b.rebind(q), b.args(a)...)
}
func (b *bound) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	return b.raw.QueryRowContext(ctx, b.rebind(q), b.args(a)...)
}

// Store is an open database: one dialect, a write handle and a read handle.
// On Postgres both are the same pool.
type Store struct {
	cfg     Config
	dialect Dialect
	write   *sql.DB
	read    *sql.DB
	guard   PanicGuard
	cache   *sync.Map

	readQ *bound
	once  *sync.Once
}

// Open builds a Store from cfg and verifies the connection.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	d, err := DialectFor(cfg.Driver)
	if err != nil {
		return nil, err
	}
	s := &Store{cfg: cfg, dialect: d, guard: cfg.Guard, cache: &sync.Map{}, once: &sync.Once{}}
	if s.guard == nil {
		s.guard = panics.Default()
	}
	switch cfg.Driver {
	case DriverSQLite:
		err = s.openSQLite(ctx)
	case DriverPostgres:
		err = s.openPostgres(ctx, cfg.Postgres.DSN)
	}
	if err != nil {
		return nil, err
	}
	s.readQ = newBound(d, s.read, s.cache)
	return s, nil
}

func (s *Store) openSQLite(ctx context.Context) error {
	c := s.cfg.SQLite
	opts := sqlitekit.WriterOptions()
	opts.Synchronous = strings.ToUpper(c.Synchronous)
	if opts.Synchronous == "" {
		opts.Synchronous = "FULL"
	}
	opts.WAL = !strings.EqualFold(c.Journal, "delete")
	if c.BusyTimeout > 0 {
		opts.BusyTimeout = c.BusyTimeout
	}
	oo := sqlitekit.OpenOptions{Options: opts, CreateParentDir: c.CreateParentDir}
	if c.Path == ":memory:" {
		opts.WAL = false
		oo.Options = opts
		db, err := sqlitekit.OpenSingle(ctx, c.Path, oo)
		if err != nil {
			return err
		}
		s.write, s.read = db, db
		return nil
	}
	w, err := sqlitekit.OpenWriter(ctx, c.Path, oo)
	if err != nil {
		return err
	}
	ro := oo
	ro.Options = opts
	ro.TxLock = ""
	ro.MaxOpenConns = c.ReadConns
	r, err := sqlitekit.OpenReader(ctx, c.Path, ro)
	if err != nil {
		_ = w.Close()
		return err
	}
	s.write, s.read = w, r
	return nil
}

func (s *Store) openPostgres(ctx context.Context, dsn string) error {
	db, err := openPG(ctx, dsn, s.cfg.Postgres)
	if err != nil {
		return err
	}
	s.write, s.read = db, db
	return nil
}

func openPG(ctx context.Context, dsn string, c PostgresConfig) (*sql.DB, error) {
	cc, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx errors can echo the DSN; do not pass them through.
		return nil, fmt.Errorf("%w: postgres dsn is not parseable", ErrBadConfig)
	}
	if !c.AllowInsecure && (cc.TLSConfig == nil || len(cc.Fallbacks) > 0 || cc.TLSConfig.InsecureSkipVerify) {
		return nil, fmt.Errorf("%w: postgres requires sslmode=verify-full (set AllowInsecure for local tests)", ErrBadConfig)
	}
	if c.ApplicationName != "" {
		if _, ok := cc.RuntimeParams["application_name"]; !ok {
			cc.RuntimeParams["application_name"] = c.ApplicationName
		}
	}
	db := stdlib.OpenDB(*cc)
	maxConns := c.MaxConns
	if maxConns <= 0 {
		maxConns = 10
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	life := c.ConnMaxLifetime
	if life <= 0 {
		life = 30 * time.Minute
	}
	db.SetConnMaxLifetime(life)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: postgres ping failed: %w", sanitizePGErr(err))
	}
	return db, nil
}

// sanitizePGErr keeps the server's error but drops anything shaped like a DSN.
func sanitizePGErr(err error) error {
	if strings.Contains(err.Error(), "password=") || strings.Contains(err.Error(), "postgres://") {
		return errors.New("connection failed (details withheld: may contain credentials)")
	}
	return err
}

// OpenMigration opens a second Postgres pool with the migration credential
// (Config.Postgres.MigrateDSN). On SQLite it returns the Store itself.
func OpenMigration(ctx context.Context, cfg Config) (*Store, bool, error) {
	if cfg.Driver != DriverPostgres || cfg.Postgres.MigrateDSN == "" {
		return nil, false, nil
	}
	m := cfg
	m.Postgres.DSN = cfg.Postgres.MigrateDSN
	m.Postgres.MaxConns = 2
	s, err := Open(ctx, m)
	return s, true, err
}

// Dialect returns the engine adapter.
func (s *Store) Dialect() Dialect { return s.dialect }

// Config returns the configuration the Store was opened with.
func (s *Store) Config() Config { return s.cfg }

// DB returns the write handle, for libraries that need a *sql.DB (goose, backup
// hooks). Statements run through it bypass rebinding: prefer the Store.
func (s *Store) DB() *sql.DB { return s.write }

// Close releases both pools.
func (s *Store) Close() error {
	var err error
	s.once.Do(func() {
		err = s.write.Close()
		if s.read != s.write {
			err = errors.Join(err, s.read.Close())
		}
	})
	return err
}

// Ping checks both pools.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.write.PingContext(ctx); err != nil {
		return err
	}
	if s.read != s.write {
		return s.read.PingContext(ctx)
	}
	return nil
}

// Reader is the read-pool Querier. Do not use it inside a WriteTx callback.
func (s *Store) Reader() Querier { return s.readQ }

// ExecContext runs a write statement outside a transaction. Prefer WriteTx for
// anything that touches more than one row.
func (s *Store) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return newBound(s.dialect, s.write, s.cache).ExecContext(ctx, q, a...)
}

// QueryContext reads from the read pool.
func (s *Store) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return s.readQ.QueryContext(ctx, q, a...)
}

// QueryRowContext reads one row from the read pool.
func (s *Store) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	return s.readQ.QueryRowContext(ctx, q, a...)
}

func (s *Store) retry() txutil.RetryOptions {
	return txutil.RetryOptions{MaxAttempts: 5, BaseDelay: 2 * time.Millisecond, MaxDelay: 100 * time.Millisecond, Jitter: true, IsRetryable: IsRetryable}
}

// WriteTx runs fn in a write transaction: IMMEDIATE on SQLite, read committed
// on Postgres. It commits when fn returns nil and rolls back on an error or a
// panic; a panic is returned as a *panics.Error with a reference. Contention
// errors (SQLITE_BUSY, serialization failure, deadlock) restart the whole
// transaction, so fn must be safe to run again from scratch.
func (s *Store) WriteTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	return txutil.WithImmediateRetry(ctx, s.write, s.retry(), func(raw *sql.Tx) error {
		tx := newBound(s.dialect, raw, s.cache)
		return s.guard.Call("store.WriteTx", func() error { return fn(ctx, tx) })
	})
}

// ReadTx runs fn in a read-only transaction on the read pool, for a consistent
// view over several queries.
func (s *Store) ReadTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) (err error) {
	raw, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: s.dialect.Name() == DriverPostgres})
	if err != nil {
		return err
	}
	defer func() { _ = raw.Rollback() }()
	return s.guard.Call("store.ReadTx", func() error { return fn(ctx, newBound(s.dialect, raw, s.cache)) })
}

// WithGuard returns a Store sharing s's pools that contains panics with g. It
// is for tests and for subsystems with their own reporter; closing either
// closes the shared pools.
func (s *Store) WithGuard(g PanicGuard) *Store {
	c := *s
	c.guard = g
	return &c
}

// WithConfig returns a Store sharing s's pools with a different Config value
// (for example a different SchemaMode). It does not reopen anything.
func (s *Store) WithConfig(cfg Config) *Store {
	c := *s
	c.cfg = cfg
	return &c
}
