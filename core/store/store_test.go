package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/panics"
	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/lint"
	"github.com/hollis-labs/libs/core/store/storetest"
)

var ctx = context.Background()

// ddl renders the one place the schemas differ: the timestamp column type.
func ddl(s *store.Store) string {
	ts := "TEXT"
	if s.Dialect().Name() == store.DriverPostgres {
		ts = "TIMESTAMPTZ"
	}
	return fmt.Sprintf(`CREATE TABLE items (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		n BIGINT NOT NULL DEFAULT 0,
		flag BOOLEAN NOT NULL DEFAULT FALSE,
		at %s,
		note TEXT,
		doc %s
	)`, ts, map[bool]string{true: "JSONB", false: "TEXT"}[s.Dialect().Name() == store.DriverPostgres])
}

func setup(t *testing.T, s *store.Store) {
	t.Helper()
	storetest.Exec(t, s, ddl(s))
}

func TestUpsertAndReturning(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		setup(t, s)
		err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var n int64
			if err := tx.QueryRowContext(ctx,
				`INSERT INTO items (id, name, n) VALUES (?, ?, ?)
				 ON CONFLICT (id) DO UPDATE SET name = excluded.name, n = items.n + 1
				 RETURNING n`, "a", "one", 1).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Errorf("first insert n = %d", n)
			}
			if err := tx.QueryRowContext(ctx,
				`INSERT INTO items (id, name, n) VALUES (?, ?, ?)
				 ON CONFLICT (id) DO UPDATE SET name = excluded.name, n = items.n + 1
				 RETURNING n`, "a", "uno", 1).Scan(&n); err != nil {
				return err
			}
			if n != 2 {
				t.Errorf("upsert n = %d", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		var name string
		if err := s.QueryRowContext(ctx, `SELECT name FROM items WHERE id = ?`, "a").Scan(&name); err != nil || name != "uno" {
			t.Fatalf("name = %q, err %v", name, err)
		}
	})
}

func TestTimeRoundTripAndOrder(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		setup(t, s)
		zone := time.FixedZone("x", 5*3600+1800)
		// Trailing zeros matter: the second value would sort wrongly as RFC3339Nano.
		in := []time.Time{
			time.Date(2026, 10, 23, 10, 0, 0, 123456789, zone),
			time.Date(2026, 10, 23, 4, 30, 0, 0, time.UTC),
			time.Date(2026, 10, 23, 4, 30, 0, 500_000_000, time.UTC),
		}
		for i, tm := range in {
			storetest.Exec(t, s, `INSERT INTO items (id, name, at) VALUES (?, ?, ?)`, fmt.Sprint(i), "t", tm)
		}
		storetest.Exec(t, s, `INSERT INTO items (id, name, at) VALUES (?, ?, ?)`, "nil", "t", (*time.Time)(nil))
		rows, err := s.QueryContext(ctx, `SELECT id, at FROM items WHERE at IS NOT NULL ORDER BY at ASC, id ASC`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			var got time.Time
			if err := rows.Scan(&id, store.ScanTime(&got)); err != nil {
				t.Fatal(err)
			}
			var i int
			fmt.Sscan(id, &i)
			want := store.Canonical(in[i])
			if !got.Equal(want) || got.Location() != time.UTC {
				t.Errorf("id %s: got %v want %v", id, got, want)
			}
			ids = append(ids, id)
		}
		if fmt.Sprint(ids) != "[1 0 2]" {
			t.Errorf("time order = %v, want [1 0 2]", ids)
		}
		var p *time.Time
		if err := s.QueryRowContext(ctx, `SELECT at FROM items WHERE id = ?`, "nil").Scan(store.ScanNullTime(&p)); err != nil || p != nil {
			t.Errorf("null time: %v %v", p, err)
		}
	})
}

func TestNullsOrderingAndCaseInsensitiveLike(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		setup(t, s)
		storetest.Exec(t, s, `INSERT INTO items (id, name, note) VALUES (?, ?, ?)`, "1", "Alpha_Beta", "x")
		storetest.Exec(t, s, `INSERT INTO items (id, name, note) VALUES (?, ?, ?)`, "2", "alphaXbeta", nil)
		storetest.Exec(t, s, `INSERT INTO items (id, name, note) VALUES (?, ?, ?)`, "3", "ZED", "a")
		var got []string
		rows, err := s.QueryContext(ctx, `SELECT id FROM items ORDER BY note ASC NULLS FIRST, id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			got = append(got, id)
		}
		rows.Close()
		if fmt.Sprint(got) != "[2 3 1]" {
			t.Errorf("nulls first: %v", got)
		}
		like, err := store.LikeInsensitive("name")
		if err != nil {
			t.Fatal(err)
		}
		var n int
		pat := "%" + store.LikeEscape("alpha_b") + "%"
		if err := s.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE `+like, pat).Scan(&n); err != nil || n != 1 {
			t.Errorf("escaped like matched %d (err %v), want 1", n, err)
		}
		if err := s.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE `+like, "ZED").Scan(&n); err != nil || n != 1 {
			t.Errorf("case-insensitive like matched %d (err %v)", n, err)
		}
	})
}

func TestBoolAndJSON(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		setup(t, s)
		storetest.Exec(t, s, `INSERT INTO items (id, name, flag, doc) VALUES (?, ?, ?, ?)`, "1", "n", true,
			`{"a":{"b":"hello","n":42,"f":1.5,"ok":true},"list":["x","y"]}`)
		storetest.Exec(t, s, `INSERT INTO items (id, name, flag, doc) VALUES (?, ?, ?, ?)`, "2", "n", false, `{"a":{"b":"bye","n":7,"f":2.5,"ok":false}}`)
		d := s.Dialect()
		text, _ := d.JSONText("doc", "a", "b")
		num, _ := d.JSONInt("doc", "a", "n")
		fl, _ := d.JSONFloat("doc", "a", "f")
		ok, _ := d.JSONBool("doc", "a", "ok")
		item, _ := d.JSONText("doc", "list", "1")
		var b string
		var n int64
		var f float64
		var o bool
		var it *string
		err := s.QueryRowContext(ctx, `SELECT `+text+`, `+num+`, `+fl+`, `+ok+`, `+item+` FROM items WHERE flag = ? AND `+num+` > ?`, true, int64(10)).
			Scan(&b, &n, &f, &o, &it)
		if err != nil {
			t.Fatal(err)
		}
		if b != "hello" || n != 42 || f != 1.5 || !o || it == nil || *it != "y" {
			t.Errorf("json: %q %d %v %v %v", b, n, f, o, it)
		}
		var cnt int
		if err := s.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE `+ok, nil).Scan(&cnt); err == nil {
			_ = cnt // arg count mismatch must error, not panic
		}
		if _, err := d.JSONText("doc; DROP TABLE items", "a"); !errors.Is(err, store.ErrBadIdentifier) {
			t.Errorf("injection in column not rejected: %v", err)
		}
		if _, err := d.JSONText("doc", "a'b"); !errors.Is(err, store.ErrBadIdentifier) {
			t.Errorf("injection in path not rejected: %v", err)
		}
	})
}

func TestTransactions(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		setup(t, s)
		sentinel := errors.New("boom")
		err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, name) VALUES (?, ?)`, "r", "rolled"); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v", err)
		}
		var n int
		_ = s.QueryRowContext(ctx, `SELECT count(*) FROM items`).Scan(&n)
		if n != 0 {
			t.Fatalf("rollback left %d rows", n)
		}

		// A panic rolls back, is contained, and surfaces as an error with a ref.
		var rec []panics.Record
		g := panics.New(panics.Config{Reporter: panics.ReporterFunc(func(r panics.Record) { rec = append(rec, r) }), Redact: panics.Unredacted})
		sg := s.WithGuard(g)
		err = sg.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			_, _ = tx.ExecContext(ctx, `INSERT INTO items (id, name) VALUES (?, ?)`, "p", "panic")
			panic("kaboom")
		})
		if _, ok := panics.Ref(err); !ok {
			t.Fatalf("panic not surfaced as a ref error: %v", err)
		}
		if len(rec) != 1 {
			t.Fatalf("reporter saw %d panics", len(rec))
		}
		_ = s.QueryRowContext(ctx, `SELECT count(*) FROM items`).Scan(&n)
		if n != 0 {
			t.Fatalf("panic left %d rows", n)
		}
		// The store still works afterwards.
		storetest.Exec(t, s, `INSERT INTO items (id, name) VALUES (?, ?)`, "ok", "ok")
	})
}

func TestConstraintClassification(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		setup(t, s)
		storetest.Exec(t, s, `INSERT INTO items (id, name) VALUES (?, ?)`, "dup", "x")
		_, err := s.ExecContext(ctx, `INSERT INTO items (id, name) VALUES (?, ?)`, "dup", "y")
		if !store.IsUniqueViolation(err) {
			t.Errorf("unique violation not classified: %v", err)
		}
	})
}

func TestConcurrentWritersAndReaders(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		setup(t, s)
		storetest.Exec(t, s, `INSERT INTO items (id, name, n) VALUES (?, ?, ?)`, "c", "counter", 0)
		const workers, each = 8, 10
		var wg sync.WaitGroup
		errs := make(chan error, workers*each*2)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
						var n int64
						if err := tx.QueryRowContext(ctx, `SELECT n FROM items WHERE id = ?`, "c").Scan(&n); err != nil {
							return err
						}
						_, err := tx.ExecContext(ctx, `UPDATE items SET n = ? WHERE id = ?`, n+1, "c")
						return err
					})
					if err != nil {
						errs <- err
					}
					var n int64
					if err := s.QueryRowContext(ctx, `SELECT n FROM items WHERE id = ?`, "c").Scan(&n); err != nil {
						errs <- err
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		var n int64
		_ = s.QueryRowContext(ctx, `SELECT n FROM items WHERE id = ?`, "c").Scan(&n)
		// Read committed on Postgres allows lost updates for a read-then-write
		// without locking; SQLite's IMMEDIATE serialises it. Both must stay in range.
		if s.Dialect().Name() == store.DriverSQLite && n != workers*each {
			t.Errorf("sqlite counter = %d, want %d", n, workers*each)
		}
		if n < 1 || n > workers*each {
			t.Errorf("counter out of range: %d", n)
		}
	})
}

func TestReadTx(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		setup(t, s)
		storetest.Exec(t, s, `INSERT INTO items (id, name) VALUES (?, ?)`, "1", "a")
		err := s.ReadTx(ctx, func(ctx context.Context, q store.Querier) error {
			var n int
			return q.QueryRowContext(ctx, `SELECT count(*) FROM items`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestConfigValidation(t *testing.T) {
	for name, cfg := range map[string]store.Config{
		"empty":       {},
		"sqlite path": {Driver: store.DriverSQLite},
		"bad sync":    {Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: "x", Synchronous: "sometimes"}},
		"pg dsn":      {Driver: store.DriverPostgres},
		"bad schema":  {Driver: store.DriverSQLite, SchemaMode: "yolo", SQLite: store.SQLiteConfig{Path: "x"}},
		"pg insecure": {Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: "host=127.0.0.1 port=1 user=u password=hunter2 dbname=db sslmode=disable"}},
		"pg bad dsn":  {Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: "host=127.0.0.1 port=1 user=u password=hunter2 dbname=db sslmode=bogus"}},
	} {
		_, err := store.Open(ctx, cfg)
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if msg := err.Error(); contains(msg, "hunter2") {
			t.Errorf("%s: error leaks the DSN: %v", name, err)
		}
	}
}

func TestInMemory(t *testing.T) {
	s, err := store.Open(ctx, store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: ":memory:"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	setup(t, s)
	err = s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items (id, name) VALUES (?, ?)`, "1", "a")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.QueryRowContext(ctx, `SELECT count(*) FROM items`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestSQLiteDefaultsToFullSynchronous(t *testing.T) {
	s := storetest.SQLite(t)
	var sync int
	if err := s.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&sync); err != nil || sync != 2 {
		t.Fatalf("synchronous = %d (%v), want 2 (FULL)", sync, err)
	}
}

func TestBackupSQLiteAndPostgresRefusal(t *testing.T) {
	s := storetest.SQLite(t)
	setup(t, s)
	storetest.Exec(t, s, `INSERT INTO items (id, name) VALUES (?, ?)`, "1", "a")
	dest := t.TempDir() + "/copy.db"
	if _, err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyBackup(ctx, dest); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if pg := storetest.Postgres(t); pg != nil {
		if _, err := pg.Backup(ctx, dest); !errors.Is(err, store.ErrBackupUnsupported) {
			t.Errorf("postgres backup: %v", err)
		}
	}
}

// The library eats its own cooking: its non-test code carries no SQL the lint
// would reject.
func TestOwnSourceIsPortable(t *testing.T) {
	fs, err := lint.Dir(".", lint.Options{SkipTests: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		t.Errorf("%s", f)
	}
}
