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
		v := s.WithConfig(cfg)
		if err := migrate.Ensure(ctx, v, src); !errors.Is(err, migrate.ErrSchemaMismatch) {
			t.Fatalf("verify mode must not migrate: %v", err)
		}
		cfg.SchemaMode = store.SchemaAuto
		a := s.WithConfig(cfg)
		if err := migrate.Ensure(ctx, a, src); err != nil {
			t.Fatal(err)
		}
		if err := migrate.Ensure(ctx, v, src); err != nil {
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
