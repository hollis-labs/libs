package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"

	"github.com/hollis-labs/libs/core/store"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

// DefaultTable is the goose version table unless Source.Table says otherwise.
const DefaultTable = "goose_db_version"

// Errors.
var (
	// ErrSchemaMismatch means the database is not at the version this build
	// expects. Behind: run the migrator. Ahead: this build is older than the
	// schema.
	ErrSchemaMismatch = errors.New("migrate: schema version mismatch")
	// ErrParity means the sqlite and postgres directories disagree.
	ErrParity = errors.New("migrate: dialect directories are not in parity")
	// ErrAlreadyApplied is returned by Baseline when the version table has
	// recorded migrations.
	ErrAlreadyApplied = errors.New("migrate: version table already has migrations")
)

// Source locates an app's migrations.
type Source struct {
	// FS holds Root/sqlite and Root/postgres.
	FS fs.FS
	// Root is the directory in FS above the per-dialect directories ("." or "").
	Root string
	// Table is the version table. Default goose_db_version. Give each module
	// sharing a database its own.
	Table string
}

func (s Source) table() string {
	if s.Table == "" {
		return DefaultTable
	}
	return s.Table
}

func (s Source) dir(d store.Driver) (fs.FS, error) {
	return fs.Sub(s.FS, path.Join(s.Root, string(d)))
}

var fileRe = regexp.MustCompile(`^(\d+)_.+\.sql$`)

// Versions lists the migration versions present for a dialect, ascending.
func (s Source) Versions(d store.Driver) ([]int64, error) {
	sub, err := s.dir(d)
	if err != nil {
		return nil, err
	}
	ents, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s migrations: %w", d, err)
	}
	var out []int64
	for _, e := range ents {
		if m := fileRe.FindStringSubmatch(e.Name()); m != nil && !e.IsDir() {
			v, _ := strconv.ParseInt(m[1], 10, 64)
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Latest is the highest version for a dialect, or 0 when there are none.
func (s Source) Latest(d store.Driver) (int64, error) {
	v, err := s.Versions(d)
	if err != nil || len(v) == 0 {
		return 0, err
	}
	return v[len(v)-1], nil
}

// CheckParity returns ErrParity unless both dialect directories contain exactly
// the same migration versions. Call it from a unit test.
func CheckParity(s Source) error {
	a, err := s.Versions(store.DriverSQLite)
	if err != nil {
		return err
	}
	b, err := s.Versions(store.DriverPostgres)
	if err != nil {
		return err
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		return fmt.Errorf("%w: sqlite %v, postgres %v", ErrParity, a, b)
	}
	return nil
}

func gooseDialect(d store.Driver) (goose.Dialect, error) {
	switch d {
	case store.DriverSQLite:
		return goose.DialectSQLite3, nil
	case store.DriverPostgres:
		return goose.DialectPostgres, nil
	}
	return "", fmt.Errorf("%w: %q", store.ErrUnknownDriver, d)
}

func provider(s *store.Store, src Source) (*goose.Provider, error) {
	d := s.Dialect().Name()
	gd, err := gooseDialect(d)
	if err != nil {
		return nil, err
	}
	sub, err := src.dir(d)
	if err != nil {
		return nil, err
	}
	opts := []goose.ProviderOption{goose.WithTableName(src.table())}
	if d == store.DriverPostgres {
		h := fnv.New64a()
		_, _ = h.Write([]byte(src.table()))
		l, err := lock.NewPostgresSessionLocker(lock.WithLockID(int64(h.Sum64() >> 1)))
		if err != nil {
			return nil, err
		}
		opts = append(opts, goose.WithSessionLocker(l))
	}
	return goose.NewProvider(gd, s.DB(), sub, opts...)
}

// UpStore applies pending migrations over s's own connection. Use [Up] when the
// migration credential differs from the runtime one.
func UpStore(ctx context.Context, s *store.Store, src Source) error {
	p, err := provider(s, src)
	if err != nil {
		return err
	}
	// Provider.Close would close the Store's pool, so it is not called.
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}

// Up applies pending migrations using the migration credential in cfg
// (Postgres.MigrateDSN) when one is set, otherwise over a fresh connection from
// the runtime settings.
func Up(ctx context.Context, cfg store.Config, src Source) error {
	m, ok, err := store.OpenMigration(ctx, cfg)
	if err != nil {
		return err
	}
	if !ok {
		m, err = store.Open(ctx, cfg)
		if err != nil {
			return err
		}
	}
	defer m.Close()
	return UpStore(ctx, m, src)
}

// Current reads the applied version without needing DDL rights. A missing
// version table is version 0.
func Current(ctx context.Context, q store.Querier, src Source) (int64, error) {
	var v sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT max(version_id) FROM `+src.table()+` WHERE is_applied = ?`, true).Scan(&v)
	if err != nil {
		if missingTable(err) {
			return 0, nil
		}
		return 0, err
	}
	return v.Int64, nil
}

func missingTable(err error) bool {
	m := err.Error()
	return contains(m, "no such table") || contains(m, "42P01") || contains(m, "does not exist")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Verify returns ErrSchemaMismatch unless the database is exactly at the latest
// version in src. It reads one row and needs no DDL rights.
func Verify(ctx context.Context, s *store.Store, src Source) error {
	want, err := src.Latest(s.Dialect().Name())
	if err != nil {
		return err
	}
	got, err := Current(ctx, s.Reader(), src)
	if err != nil {
		return fmt.Errorf("migrate: read schema version: %w", err)
	}
	if got != want {
		return fmt.Errorf("%w: database at %d, build expects %d", ErrSchemaMismatch, got, want)
	}
	return nil
}

// Ensure applies Config.SchemaMode: auto migrates (over the migration
// credential when configured) and then verifies; verify only verifies.
func Ensure(ctx context.Context, s *store.Store, src Source) error {
	cfg := s.Config()
	if cfg.EffectiveSchemaMode() == store.SchemaAuto {
		if cfg.Postgres.MigrateDSN != "" && cfg.Driver == store.DriverPostgres {
			if err := Up(ctx, cfg, src); err != nil {
				return err
			}
		} else if err := UpStore(ctx, s, src); err != nil {
			return err
		}
	}
	return Verify(ctx, s, src)
}

// Baseline adopts a database whose schema already exists at version upTo (for
// example one managed by a custom version table): it creates the goose table and
// records every migration up to and including upTo as applied, without running
// them. It refuses when the table already records migrations. Verify the
// existing schema matches the migrations first; Baseline cannot.
func Baseline(ctx context.Context, s *store.Store, src Source, upTo int64) error {
	d := s.Dialect().Name()
	gd, err := gooseDialect(d)
	if err != nil {
		return err
	}
	gs, err := database.NewStore(database.Dialect(gd), src.table())
	if err != nil {
		return err
	}
	versions, err := src.Versions(d)
	if err != nil {
		return err
	}
	var applied int
	exists := true
	if err := s.Reader().QueryRowContext(ctx, `SELECT count(*) FROM `+src.table()+` WHERE version_id > ?`, 0).Scan(&applied); err != nil {
		if !missingTable(err) {
			return err
		}
		exists = false
	}
	if applied > 0 {
		return ErrAlreadyApplied
	}
	return s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		conn := txConn{tx}
		if !exists {
			if err := gs.CreateVersionTable(ctx, conn); err != nil {
				return err
			}
			// goose's own bootstrap records version 0 right after creating the table.
			if err := gs.Insert(ctx, conn, database.InsertRequest{Version: 0}); err != nil {
				return err
			}
		}
		for _, v := range versions {
			if v > upTo {
				break
			}
			if err := gs.Insert(ctx, conn, database.InsertRequest{Version: v}); err != nil {
				return err
			}
		}
		return nil
	})
}

// txConn lets goose's store run statements through the Store's transaction,
// which rebinds placeholders; goose already writes dialect-correct SQL, and
// Rebind leaves "$n" alone, so this only adapts the type.
type txConn struct{ q store.Querier }

func (c txConn) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return c.q.ExecContext(ctx, q, a...)
}
func (c txConn) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return c.q.QueryContext(ctx, q, a...)
}
func (c txConn) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	return c.q.QueryRowContext(ctx, q, a...)
}
