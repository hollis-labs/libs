// Package storetest runs tests against every supported engine. SQLite always
// runs. Postgres runs when GOSTORE_TEST_PG_DSN is set; without it the Postgres
// subtests skip locally and FAIL when GOSTORE_REQUIRE_PG=1 or CI is set, so a
// CI run can never pass without exercising Postgres.
//
// An app adopting go-store calls [EachDialect] from its own store tests to run
// the same test body on both engines.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Env names read by the package.
const (
	EnvDSN     = "GOSTORE_TEST_PG_DSN"
	EnvRequire = "GOSTORE_REQUIRE_PG"
)

// EachDialect runs fn once per engine, each on a fresh empty database.
func EachDialect(t *testing.T, fn func(t *testing.T, s *store.Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, SQLite(t)) })
	t.Run("postgres", func(t *testing.T) {
		s := Postgres(t)
		if s == nil {
			return
		}
		fn(t, s)
	})
}

// SQLite opens a fresh file database in the test's temp directory.
func SQLite(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), store.Config{
		Driver: store.DriverSQLite,
		SQLite: store.SQLiteConfig{Path: t.TempDir() + "/test.db", ReadConns: 2},
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ciSet reports whether CI is truthy: set, and not "", "0" or "false".
func ciSet() bool {
	switch strings.ToLower(os.Getenv("CI")) {
	case "", "0", "false":
		return false
	}
	return true
}

// PGConfig returns a Config for a fresh, private schema on the test server, or
// nil after skipping the test when no server is configured. The schema is
// dropped when the test ends.
func PGConfig(t *testing.T) *store.Config {
	t.Helper()
	dsn := os.Getenv(EnvDSN)
	if dsn == "" {
		if os.Getenv(EnvRequire) == "1" || ciSet() {
			t.Fatalf("%s is not set and Postgres is required here (%s=1 or CI)", EnvDSN, EnvRequire)
		}
		t.Skipf("%s not set; skipping Postgres", EnvDSN)
		return nil
	}
	ctx := context.Background()
	cc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvDSN, err)
	}
	admin := stdlib.OpenDB(*cc)
	defer admin.Close()
	// Extensions the search package uses. Idempotent; a concurrent creator can
	// lose the race harmlessly.
	for _, ext := range []string{"pg_trgm", "unaccent"} {
		if _, err := admin.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS "+ext); err != nil {
			if !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "duplicate key") {
				t.Fatalf("create extension %s: %v", ext, err)
			}
		}
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "gs_" + hex.EncodeToString(b)
	mustExec(t, admin, "CREATE SCHEMA "+schema)
	t.Cleanup(func() {
		a := stdlib.OpenDB(*cc)
		defer a.Close()
		if _, err := a.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema %s: %v", schema, err)
		}
	})
	return &store.Config{
		Driver:   store.DriverPostgres,
		Postgres: store.PostgresConfig{DSN: withSearchPath(dsn, schema), AllowInsecure: true, MaxConns: 8},
	}
}

// Postgres opens a Store on a fresh schema, or returns nil after skipping.
func Postgres(t *testing.T) *store.Store {
	t.Helper()
	cfg := PGConfig(t)
	if cfg == nil {
		return nil
	}
	s, err := store.Open(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func withSearchPath(dsn, schema string) string {
	sp := schema + ",public"
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "search_path=" + sp
	}
	return dsn + " search_path=" + sp
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// Exec is a test convenience: run a statement on the Store's write handle and
// fail the test on error.
func Exec(t *testing.T, s *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := s.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
