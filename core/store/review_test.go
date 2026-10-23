package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/storetest"
)

func TestReadTxIsOneSnapshot(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		storetest.Exec(t, s, `CREATE TABLE snap (id INTEGER PRIMARY KEY)`)
		storetest.Exec(t, s, `INSERT INTO snap (id) VALUES (?)`, 1)
		err := s.ReadTx(context.Background(), func(ctx context.Context, q store.Querier) error {
			count := func() int {
				var n int
				if err := q.QueryRowContext(ctx, `SELECT count(*) FROM snap`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := count()
			// A write committed by someone else in the middle of the read.
			storetest.Exec(t, s, `INSERT INTO snap (id) VALUES (?)`, 2)
			if after := count(); after != before {
				t.Errorf("the view moved inside one ReadTx: %d then %d", before, after)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestNullTimeAndValuersAreEncodedLikeTime(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		storetest.Exec(t, s, `CREATE TABLE ev (id INTEGER PRIMARY KEY, at `+timeType(s)+`)`)
		zone := time.FixedZone("far", 9*3600+1800)
		early := time.Date(2026, 10, 23, 9, 0, 0, 123456789, zone)
		late := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC) // later than early in UTC? early = 23:30 UTC the day before
		storetest.Exec(t, s, `INSERT INTO ev (id, at) VALUES (?, ?)`, 1, sql.NullTime{Time: early, Valid: true})
		storetest.Exec(t, s, `INSERT INTO ev (id, at) VALUES (?, ?)`, 2, sql.NullTime{Time: late, Valid: true})
		storetest.Exec(t, s, `INSERT INTO ev (id, at) VALUES (?, ?)`, 3, sql.NullTime{})
		rows, err := s.QueryContext(context.Background(), `SELECT id, at FROM ev WHERE at IS NOT NULL ORDER BY at ASC`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var order []int
		var first time.Time
		for rows.Next() {
			var id int
			var at time.Time
			if err := rows.Scan(&id, store.ScanTime(&at)); err != nil {
				t.Fatalf("a NullTime was stored in a form ScanTime cannot read: %v", err)
			}
			if len(order) == 0 {
				first = at
			}
			order = append(order, id)
		}
		if fmt.Sprint(order) != "[1 2]" {
			t.Errorf("order = %v, want [1 2] (early is 23:30 UTC the day before)", order)
		}
		if !first.Equal(store.Canonical(early)) {
			t.Errorf("round trip: %v != %v", first, store.Canonical(early))
		}
	})
}

func timeType(s *store.Store) string {
	if s.Dialect().Name() == store.DriverPostgres {
		return "TIMESTAMPTZ"
	}
	return "TEXT"
}

func TestConfigNeverPrintsCredentials(t *testing.T) {
	cfg := store.Config{
		Driver: store.DriverPostgres,
		Postgres: store.PostgresConfig{
			DSN:        "host=db user=app password=hunter2 dbname=x",
			MigrateDSN: "host=db user=owner password=s3cret dbname=x",
		},
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("opening", "config", cfg, "pg", cfg.Postgres)
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", cfg), "%+v": fmt.Sprintf("%+v", cfg), "%#v": fmt.Sprintf("%#v", cfg),
		"%s": fmt.Sprintf("%s", cfg), "pg %+v": fmt.Sprintf("%+v", cfg.Postgres), "slog": buf.String(),
		"ptr": fmt.Sprintf("%+v", &cfg),
	} {
		if strings.Contains(out, "hunter2") || strings.Contains(out, "s3cret") {
			t.Errorf("%s leaked a password: %s", name, out)
		}
		if !strings.Contains(out, "redacted") {
			t.Errorf("%s did not show that the DSN is redacted: %s", name, out)
		}
	}
}

func TestOpenStoreKeepsNoConnectionStrings(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		c := s.Config()
		if c.Postgres.DSN != "" || c.Postgres.MigrateDSN != "" {
			t.Fatalf("the Store retained connection strings: %+v", c.Postgres)
		}
	})
}

func TestMemoryURIsShareOneConnection(t *testing.T) {
	for _, path := range []string{":memory:", "file::memory:"} {
		s, err := store.Open(context.Background(), store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: path}})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		storetest.Exec(t, s, `CREATE TABLE m (id INTEGER)`)
		storetest.Exec(t, s, `INSERT INTO m (id) VALUES (1)`)
		var n int
		if err := s.QueryRowContext(context.Background(), `SELECT count(*) FROM m`).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: reader did not see the writer's table: n=%d err=%v", path, n, err)
		}
		_ = s.Close()
	}
}

func TestSQLiteReaderRejectsWrites(t *testing.T) {
	s := storetest.SQLite(t)
	storetest.Exec(t, s, `CREATE TABLE r (id INTEGER)`)
	if _, err := s.Reader().ExecContext(context.Background(), `INSERT INTO r (id) VALUES (1)`); err == nil {
		t.Fatal("a write through the read pool must fail")
	}
}

func TestManyDistinctStatementsDoNotGrowWithoutBound(t *testing.T) {
	s := storetest.SQLite(t)
	storetest.Exec(t, s, `CREATE TABLE c (id INTEGER)`)
	for i := 0; i < 3000; i++ {
		var n int
		q := fmt.Sprintf(`SELECT count(*) FROM c WHERE id = ? AND %d = %d`, i, i)
		if err := s.QueryRowContext(context.Background(), q, i).Scan(&n); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLiteLowerFoldsUnicode(t *testing.T) {
	s := storetest.SQLite(t)
	var got string
	if err := s.QueryRowContext(context.Background(), `SELECT `+s.Dialect().Lower("?"), "CAFÉ ÀÖ").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "café àö" {
		t.Fatalf("lower = %q", got)
	}
}

func TestGoexitInsideWriteTxReleasesTheWriter(t *testing.T) {
	s := storetest.SQLite(t)
	storetest.Exec(t, s, `CREATE TABLE g (id INTEGER)`)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.WriteTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
			_, _ = tx.ExecContext(ctx, `INSERT INTO g (id) VALUES (1)`)
			runtime.Goexit()
			return nil
		})
	}()
	<-done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO g (id) VALUES (2)`)
		return err
	})
	if err != nil {
		t.Fatalf("the writer stayed held after Goexit: %v", err)
	}
	var n int
	_ = s.QueryRowContext(context.Background(), `SELECT count(*) FROM g`).Scan(&n)
	if n != 1 {
		t.Errorf("the Goexit transaction must roll back; rows = %d", n)
	}
}

func TestPostgresTLSIsCheckedPerHost(t *testing.T) {
	open := func(dsn string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := store.Open(ctx, store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: dsn}})
		return err
	}
	for _, c := range []struct {
		name, dsn string
		refused   bool
	}{
		{"one host, prefer", "host=db1.invalid user=u dbname=d sslmode=prefer", true},
		{"two hosts, both verify-full", "host=db1.invalid,db2.invalid user=u dbname=d sslmode=verify-full", false},
		{"two hosts, require", "host=db1.invalid,db2.invalid user=u dbname=d sslmode=require", true},
		{"unix socket", "host=/nonexistent/sock user=u dbname=d", false},
		{"socket and a tcp host without TLS", "host=/nonexistent/sock,db1.invalid user=u dbname=d sslmode=disable", true},
	} {
		err := open(c.dsn)
		if got := errors.Is(err, store.ErrBadConfig); got != c.refused {
			t.Errorf("%s: refused=%v, want %v (err: %v)", c.name, got, c.refused, err)
		}
	}
}

type countingValuer struct{ calls *int }

func (c countingValuer) Value() (driver.Value, error) { *c.calls++; return "v", nil }

func TestValuerIsEvaluatedOnce(t *testing.T) {
	s := storetest.SQLite(t)
	storetest.Exec(t, s, `CREATE TABLE vv (v TEXT)`)
	n := 0
	storetest.Exec(t, s, `INSERT INTO vv (v) VALUES (?)`, countingValuer{&n})
	if n != 1 {
		t.Fatalf("Value() called %d times", n)
	}
}

func TestConfigJSONOmitsConnectionStrings(t *testing.T) {
	b, err := json.Marshal(store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: "password=hunter2", MigrateDSN: "password=s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "s3cret") {
		t.Fatalf("json leaked: %s", b)
	}
}
