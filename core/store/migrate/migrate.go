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
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hollis-labs/libs/core/store"
	"github.com/jackc/pgx/v5/pgconn"
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

// checkedTable returns the version table name, which is spliced into SQL and so
// must be a plain identifier.
func (s Source) checkedTable() (string, error) {
	t := s.Table
	if t == "" {
		t = DefaultTable
	}
	if err := store.CheckIdentifier(t); err != nil || strings.Contains(t, ".") {
		return "", fmt.Errorf("migrate: version table %q must be a plain, unqualified identifier (choose the schema with search_path)", t)
	}
	return t, nil
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

// lockID derives the Postgres advisory-lock key from the version table, so
// goose's session lock and Baseline's transaction lock contend on the same key.
func lockID(table string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(table))
	return int64(h.Sum64() >> 1)
}

func provider(s *store.Store, src Source) (*goose.Provider, error) {
	table, err := src.checkedTable()
	if err != nil {
		return nil, err
	}
	d := s.Dialect().Name()
	gd, err := gooseDialect(d)
	if err != nil {
		return nil, err
	}
	sub, err := src.dir(d)
	if err != nil {
		return nil, err
	}
	opts := []goose.ProviderOption{goose.WithTableName(table)}
	if d == store.DriverPostgres {
		l, err := lock.NewPostgresSessionLocker(lock.WithLockID(lockID(table)))
		if err != nil {
			return nil, err
		}
		opts = append(opts, goose.WithSessionLocker(l))
	}
	return goose.NewProvider(gd, s.DB(), sub, opts...)
}

// UpStore applies pending migrations over s's own connection. Use [Up] when the
// migration credential differs from the runtime one.
//
// On Postgres goose holds an advisory lock, so concurrent starters queue and the
// later ones find nothing to do. SQLite has no such lock: two processes
// migrating the same file can race, and the loser fails to start (it hits a
// busy or already-exists error) without corrupting the database. Run the
// migrator from one process, or retry the start.
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

// Current reads the highest applied version without needing DDL rights. A
// missing version table is version 0.
func Current(ctx context.Context, q store.Querier, src Source) (int64, error) {
	applied, err := appliedVersions(ctx, q, src)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, v := range applied {
		if v > max {
			max = v
		}
	}
	return max, nil
}

// appliedVersions lists the applied migration versions (not goose's bootstrap
// version 0). A missing version table is the empty set.
func appliedVersions(ctx context.Context, q store.Querier, src Source) ([]int64, error) {
	table, err := src.checkedTable()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT version_id FROM `+table+` WHERE is_applied = ? AND version_id > ?`, true, 0)
	if err != nil {
		if missingTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// missingTable is true for "the table is not there": SQLSTATE 42P01 on
// Postgres, "no such table" on SQLite. Other "does not exist" errors (a missing
// column, a missing role) are real failures.
func missingTable(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "42P01"
	}
	return strings.Contains(err.Error(), "no such table")
}

// Verify returns ErrSchemaMismatch unless the applied migrations are exactly
// the versions in src: a database that is behind, ahead, or missing a version in
// the middle (0001 and 0003 applied, 0002 added later) all fail. It reads the
// version table and needs no DDL rights.
func Verify(ctx context.Context, s *store.Store, src Source) error {
	want, err := src.Versions(s.Dialect().Name())
	if err != nil {
		return err
	}
	got, err := appliedVersions(ctx, s.Reader(), src)
	if err != nil {
		return fmt.Errorf("migrate: read schema version: %w", err)
	}
	have := map[int64]bool{}
	for _, v := range got {
		have[v] = true
	}
	var missing, extra []int64
	for _, v := range want {
		if !have[v] {
			missing = append(missing, v)
		}
		delete(have, v)
	}
	for v := range have {
		extra = append(extra, v)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	if len(missing) > 0 || len(extra) > 0 {
		cur, _ := Current(ctx, s.Reader(), src)
		return fmt.Errorf("%w: database at %d; not applied %v; applied but unknown to this build %v", ErrSchemaMismatch, cur, missing, extra)
	}
	return nil
}

// Ensure applies cfg's SchemaMode to s: auto migrates (over the migration
// credential when cfg.Postgres.MigrateDSN is set) and then verifies; verify only
// verifies. cfg is the Config s was opened with; the Store itself does not keep
// its connection strings.
func Ensure(ctx context.Context, cfg store.Config, s *store.Store, src Source) error {
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
// them. upTo must be one of the versions in src. It refuses when the table
// already records migrations; the check and the writes share one transaction,
// and on Postgres one advisory lock, so two concurrent baselines cannot both
// pass. Verify the existing schema matches the migrations first; Baseline cannot.
func Baseline(ctx context.Context, s *store.Store, src Source, upTo int64) error {
	table, err := src.checkedTable()
	if err != nil {
		return err
	}
	d := s.Dialect().Name()
	gd, err := gooseDialect(d)
	if err != nil {
		return err
	}
	gs, err := database.NewStore(database.Dialect(gd), table)
	if err != nil {
		return err
	}
	versions, err := src.Versions(d)
	if err != nil {
		return err
	}
	if !slices.Contains(versions, upTo) {
		return fmt.Errorf("migrate: baseline version %d is not a %s migration in the source (have %v)", upTo, d, versions)
	}
	return s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if d == store.DriverPostgres {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, lockID(table)); err != nil {
				return err
			}
		}
		exists, err := tableExists(ctx, tx, d, table)
		if err != nil {
			return err
		}
		if exists {
			var applied int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE version_id > ?`, 0).Scan(&applied); err != nil {
				return err
			}
			if applied > 0 {
				return ErrAlreadyApplied
			}
		}
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

// tableExists asks the catalog, so a missing table does not abort a Postgres
// transaction the way a failed SELECT would.
func tableExists(ctx context.Context, q store.Querier, d store.Driver, table string) (bool, error) {
	var n int
	var err error
	switch d {
	case store.DriverPostgres:
		err = q.QueryRowContext(ctx, `SELECT count(*) FROM pg_class WHERE oid = to_regclass(?)`, table).Scan(&n)
	default:
		err = q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
	}
	return n > 0, err
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
