package store

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Driver selects the engine behind a [Store].
type Driver string

// Supported drivers.
const (
	DriverSQLite   Driver = "sqlite"
	DriverPostgres Driver = "postgres"
)

// SchemaMode says what a process does about the schema at startup.
type SchemaMode string

// Schema modes. Auto applies pending migrations with the migration credential;
// Verify only checks that the schema is at the version this build expects, which
// is what a DML-only runtime role can do.
const (
	SchemaAuto   SchemaMode = "auto"
	SchemaVerify SchemaMode = "verify"
)

// Config is the one configuration struct for both engines. Exactly the section
// that matches Driver is read.
type Config struct {
	Driver Driver
	// SchemaMode is read by migrate.Ensure. Empty means auto for SQLite and
	// verify for Postgres: a shared server migrates deliberately.
	SchemaMode SchemaMode
	SQLite     SQLiteConfig
	Postgres   PostgresConfig
	// Guard contains panics in transaction callbacks. Nil uses panics.Default().
	Guard PanicGuard
}

// SQLiteConfig configures the SQLite engine.
type SQLiteConfig struct {
	// Path is the database file, or ":memory:" for a private in-memory database
	// (one connection serves reads and writes).
	Path string
	// Synchronous is the synchronous pragma: OFF, NORMAL, FULL or EXTRA.
	// Default FULL; relax it knowingly.
	Synchronous string
	// Journal is "wal" (default) or "delete".
	Journal string
	// BusyTimeout bounds waiting for another process's lock. Default 5s.
	BusyTimeout time.Duration
	// ReadConns is the read pool size. Default 4.
	ReadConns int
	// CreateParentDir creates the database directory when missing.
	CreateParentDir bool
}

// PostgresConfig configures the Postgres engine.
type PostgresConfig struct {
	// DSN is the runtime connection string (DML-only role in production).
	DSN string `json:"-"`
	// MigrateDSN is the schema-owner connection string used only by
	// migrate.Up. Empty means migrations run over DSN.
	MigrateDSN string `json:"-"`
	// AllowInsecure permits a connection without verified TLS (tests, a local
	// container). Without it Open requires sslmode=verify-full.
	AllowInsecure bool
	// MaxConns is the pool size. Default 10.
	MaxConns int
	// ConnMaxLifetime recycles pooled connections. Default 30m.
	ConnMaxLifetime time.Duration
	// ApplicationName is set on the session when the DSN does not.
	ApplicationName string
}

// Configuration errors.
var (
	ErrUnknownDriver = errors.New("store: unknown driver")
	ErrBadConfig     = errors.New("store: invalid config")
)

func (c Config) validate() error {
	switch c.Driver {
	case DriverSQLite:
		if c.SQLite.Path == "" {
			return fmt.Errorf("%w: sqlite path is empty", ErrBadConfig)
		}
		if strings.Contains(c.SQLite.Path, "mode=memory") {
			// sqlitekit would create real files with that name.
			return fmt.Errorf("%w: sqlite path %q is an in-memory URI sqlitekit cannot open; use \":memory:\" or a file", ErrBadConfig, c.SQLite.Path)
		}
		switch strings.ToUpper(c.SQLite.Synchronous) {
		case "", "OFF", "NORMAL", "FULL", "EXTRA":
		default:
			return fmt.Errorf("%w: sqlite synchronous %q", ErrBadConfig, c.SQLite.Synchronous)
		}
		switch strings.ToLower(c.SQLite.Journal) {
		case "", "wal", "delete":
		default:
			return fmt.Errorf("%w: sqlite journal %q", ErrBadConfig, c.SQLite.Journal)
		}
	case DriverPostgres:
		if c.Postgres.DSN == "" {
			return fmt.Errorf("%w: postgres dsn is empty", ErrBadConfig)
		}
	default:
		return fmt.Errorf("%w: %q", ErrUnknownDriver, c.Driver)
	}
	switch c.SchemaMode {
	case "", SchemaAuto, SchemaVerify:
	default:
		return fmt.Errorf("%w: schema mode %q", ErrBadConfig, c.SchemaMode)
	}
	return nil
}

// EffectiveSchemaMode resolves the empty default.
func (c Config) EffectiveSchemaMode() SchemaMode {
	if c.SchemaMode != "" {
		return c.SchemaMode
	}
	if c.Driver == DriverPostgres {
		return SchemaVerify
	}
	return SchemaAuto
}

const redacted = "<redacted>"

func redact(s string) string {
	if s == "" {
		return ""
	}
	return redacted
}

// Redacted returns c with every connection string replaced, safe to log.
func (c Config) Redacted() Config {
	c.Postgres.DSN, c.Postgres.MigrateDSN = redact(c.Postgres.DSN), redact(c.Postgres.MigrateDSN)
	return c
}

// The formatting and logging hooks below keep a DSN with a password out of
// logs, %v/%+v/%#v output and slog records.

type plainConfig Config // no methods: breaks the String recursion

func (c Config) String() string   { return fmt.Sprintf("%+v", plainConfig(c.Redacted())) }
func (c Config) GoString() string { return "store.Config" + c.String() }
func (c Config) LogValue() slog.Value {
	r := c.Redacted()
	return slog.GroupValue(
		slog.String("driver", string(r.Driver)),
		slog.String("schema_mode", string(r.SchemaMode)),
		slog.String("sqlite_path", r.SQLite.Path),
		slog.String("postgres_dsn", r.Postgres.DSN),
		slog.String("postgres_migrate_dsn", r.Postgres.MigrateDSN),
	)
}

type plainPostgres PostgresConfig

func (p PostgresConfig) String() string {
	p.DSN, p.MigrateDSN = redact(p.DSN), redact(p.MigrateDSN)
	return fmt.Sprintf("%+v", plainPostgres(p))
}
func (p PostgresConfig) GoString() string { return "store.PostgresConfig" + p.String() }
func (p PostgresConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("dsn", redact(p.DSN)), slog.String("migrate_dsn", redact(p.MigrateDSN)), slog.Bool("allow_insecure", p.AllowInsecure))
}
