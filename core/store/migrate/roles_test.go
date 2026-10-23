package migrate_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/hollis-labs/libs/core/store/storetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// TestSeparateMigrationAndRuntimeRoles is the D6 contract: the schema owner
// migrates, the DML-only runtime role only verifies and reads/writes data.
func TestSeparateMigrationAndRuntimeRoles(t *testing.T) {
	base := storetest.PGConfig(t)
	if base == nil {
		return
	}
	cc, err := pgx.ParseConfig(base.Postgres.DSN)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cc)
	t.Cleanup(func() { _ = admin.Close() })
	schema := cc.RuntimeParams["search_path"]
	schema = schema[:len(schema)-len(",public")]

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	role := "gs_rt_" + hex.EncodeToString(b)
	mustExec(t, admin, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'rt-pass'", role))
	t.Cleanup(func() {
		a := stdlib.OpenDB(*cc)
		defer a.Close()
		_, _ = a.Exec("DROP OWNED BY " + role)
		_, _ = a.Exec("DROP ROLE IF EXISTS " + role)
	})
	mustExec(t, admin, fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, role))
	mustExec(t, admin, fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", schema, role))

	roleDSN := fmt.Sprintf("host=%s port=%d user=%s password=rt-pass dbname=%s search_path=%s,public sslmode=disable",
		cc.Host, cc.Port, role, cc.Database, schema)
	cfg := *base
	cfg.SchemaMode = store.SchemaVerify
	cfg.Postgres.MigrateDSN = base.Postgres.DSN // the owner (superuser here)
	cfg.Postgres.DSN = roleDSN                  // the DML-only runtime role

	rt, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// The runtime role cannot create the version table or any table.
	if err := migrate.UpStore(ctx, rt, src); err == nil {
		t.Fatal("DML-only role must not be able to migrate")
	}
	// Verify mode reports the missing schema without needing DDL rights.
	if err := migrate.Ensure(ctx, rt, src); !errors.Is(err, migrate.ErrSchemaMismatch) {
		t.Fatalf("verify before migration: %v", err)
	}
	// The migration role applies the schema; auto mode would do this at startup.
	if err := migrate.Up(ctx, cfg, src); err != nil {
		t.Fatalf("up with the migration role: %v", err)
	}
	if err := migrate.Ensure(ctx, rt, src); err != nil {
		t.Fatalf("verify after migration: %v", err)
	}
	// And the runtime role can use the tables but not alter them.
	storetest.Exec(t, rt, `INSERT INTO notes (id, body, tag) VALUES (?, ?, ?)`, "1", "b", "t")
	if _, err := rt.ExecContext(ctx, `ALTER TABLE notes ADD COLUMN extra TEXT`); err == nil {
		t.Fatal("DML-only role must not alter tables")
	}
	// Auto mode with a MigrateDSN migrates over the owner and verifies over the runtime role.
	auto := cfg
	auto.SchemaMode = store.SchemaAuto
	if err := migrate.Ensure(ctx, rt.WithConfig(auto), src); err != nil {
		t.Fatalf("auto ensure: %v", err)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
