package migrate_test

import (
	"context"
	"embed"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/hollis-labs/libs/core/store/storetest"
)

//go:embed testdata/migrations
var fixtures embed.FS

var src = migrate.Source{FS: fixtures, Root: "testdata/migrations"}

var ctx = context.Background()

func TestParity(t *testing.T) {
	if err := migrate.CheckParity(src); err != nil {
		t.Fatal(err)
	}
	bad := migrate.Source{FS: fstest.MapFS{
		"sqlite/0001_a.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
		"sqlite/0002_b.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
		"postgres/0001_a.sql": {Data: []byte("-- +goose Up\nSELECT 1;")},
	}}
	if err := migrate.CheckParity(bad); !errors.Is(err, migrate.ErrParity) {
		t.Fatalf("expected ErrParity, got %v", err)
	}
}

func TestUpVerifyEnsure(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		if err := migrate.Verify(ctx, s, src); !errors.Is(err, migrate.ErrSchemaMismatch) {
			t.Fatalf("fresh database should mismatch, got %v", err)
		}
		if err := migrate.UpStore(ctx, s, src); err != nil {
			t.Fatal(err)
		}
		if err := migrate.Verify(ctx, s, src); err != nil {
			t.Fatal(err)
		}
		// Idempotent.
		if err := migrate.UpStore(ctx, s, src); err != nil {
			t.Fatal(err)
		}
		storetest.Exec(t, s, `INSERT INTO notes (id, body, tag) VALUES (?, ?, ?)`, "1", "b", "t")

		// A build that knows a newer version than the database refuses to start in verify mode.
		newer := migrate.Source{FS: fstest.MapFS{
			"m/sqlite/0001_a.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
			"m/sqlite/0009_z.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
			"m/postgres/0001_a.sql": {Data: []byte("-- +goose Up\nSELECT 1;")},
			"m/postgres/0009_z.sql": {Data: []byte("-- +goose Up\nSELECT 1;")},
		}, Root: "m"}
		if err := migrate.Verify(ctx, s, newer); !errors.Is(err, migrate.ErrSchemaMismatch) {
			t.Fatalf("expected mismatch, got %v", err)
		}
	})
}

func TestEnsureModes(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		cfg := s.Config()
		cfg.SchemaMode = store.SchemaVerify
		verify := cfg
		if err := migrate.Ensure(ctx, verify, s, src); !errors.Is(err, migrate.ErrSchemaMismatch) {
			t.Fatalf("verify mode must not migrate: %v", err)
		}
		cfg.SchemaMode = store.SchemaAuto
		if err := migrate.Ensure(ctx, cfg, s, src); err != nil {
			t.Fatal(err)
		}
		if err := migrate.Ensure(ctx, verify, s, src); err != nil {
			t.Fatalf("verify after auto: %v", err)
		}
	})
}

func TestBaseline(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		// The app's schema pre-exists at version 1, created outside goose.
		storetest.Exec(t, s, `CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT NOT NULL)`)
		if err := migrate.Baseline(ctx, s, src, 1); err != nil {
			t.Fatal(err)
		}
		if v, err := migrate.Current(ctx, s.Reader(), src); err != nil || v != 1 {
			t.Fatalf("current = %d, %v", v, err)
		}
		if err := migrate.Baseline(ctx, s, src, 1); !errors.Is(err, migrate.ErrAlreadyApplied) {
			t.Fatalf("second baseline: %v", err)
		}
		// Only migration 2 runs now.
		if err := migrate.UpStore(ctx, s, src); err != nil {
			t.Fatal(err)
		}
		if err := migrate.Verify(ctx, s, src); err != nil {
			t.Fatal(err)
		}
		storetest.Exec(t, s, `INSERT INTO notes (id, body, tag) VALUES (?, ?, ?)`, "1", "b", "t")
	})
}

func TestVerifyDetectsGap(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		if err := migrate.UpStore(ctx, s, src); err != nil {
			t.Fatal(err)
		}
		// A build that also ships a version below the latest applied one (0001 and
		// 0003 applied, 0002 added later) must not pass on max(version) alone.
		gap := migrate.Source{FS: fstest.MapFS{
			"sqlite/0001_a.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
			"sqlite/0002_b.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
			"sqlite/0003_c.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
			"postgres/0001_a.sql": {Data: []byte("-- +goose Up\nSELECT 1;")},
			"postgres/0002_b.sql": {Data: []byte("-- +goose Up\nSELECT 1;")},
			"postgres/0003_c.sql": {Data: []byte("-- +goose Up\nSELECT 1;")},
		}, Table: "gap_versions"}
		// Apply 0001 and 0003 only, through a source that skips 0002.
		skip := migrate.Source{FS: fstest.MapFS{
			"sqlite/0001_a.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
			"sqlite/0003_c.sql":   {Data: []byte("-- +goose Up\nSELECT 1;")},
			"postgres/0001_a.sql": {Data: []byte("-- +goose Up\nSELECT 1;")},
			"postgres/0003_c.sql": {Data: []byte("-- +goose Up\nSELECT 1;")},
		}, Table: "gap_versions"}
		if err := migrate.UpStore(ctx, s, skip); err != nil {
			t.Fatal(err)
		}
		if err := migrate.Verify(ctx, s, skip); err != nil {
			t.Fatalf("exact set must verify: %v", err)
		}
		if err := migrate.Verify(ctx, s, gap); !errors.Is(err, migrate.ErrSchemaMismatch) {
			t.Fatalf("a never-applied lower version must fail verify, got %v", err)
		}
	})
}

func TestBaselineChecks(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		if err := migrate.Baseline(ctx, s, src, 7); err == nil {
			t.Fatal("baseline to a version that is not in the source must fail")
		}
		if err := migrate.Up(ctx, store.Config{}, migrate.Source{}); err == nil {
			t.Fatal("empty config must fail")
		}
		bad := migrate.Source{FS: fixtures, Root: "testdata/migrations", Table: "x; DROP TABLE y"}
		if err := migrate.Baseline(ctx, s, bad, 1); err == nil {
			t.Fatal("a version table name that is not an identifier must fail")
		}
	})
}

func TestConcurrentBaseline(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		storetest.Exec(t, s, `CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT NOT NULL)`)
		const n = 6
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			go func() { errs <- migrate.Baseline(ctx, s, src, 1) }()
		}
		ok, already := 0, 0
		for i := 0; i < n; i++ {
			switch err := <-errs; {
			case err == nil:
				ok++
			case errors.Is(err, migrate.ErrAlreadyApplied):
				already++
			default:
				t.Errorf("unexpected baseline error: %v", err)
			}
		}
		if ok != 1 || already != n-1 {
			t.Fatalf("exactly one baseline should win: ok=%d already=%d", ok, already)
		}
	})
}

// On Postgres goose's advisory lock makes concurrent starters queue, so all
// succeed. SQLite has no such lock: racing starters may lose with a busy or
// already-exists error, but must leave a database that a later start completes.
func TestConcurrentUp(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		const n = 5
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			go func() { errs <- migrate.UpStore(ctx, s, src) }()
		}
		failed := 0
		for i := 0; i < n; i++ {
			if err := <-errs; err != nil {
				failed++
				if s.Dialect().Name() == store.DriverPostgres {
					t.Errorf("postgres starters must queue on the advisory lock: %v", err)
				}
			}
		}
		if err := migrate.UpStore(ctx, s, src); err != nil {
			t.Fatalf("a later start must complete (%d racing starters failed): %v", failed, err)
		}
		if err := migrate.Verify(ctx, s, src); err != nil {
			t.Fatal(err)
		}
	})
}
