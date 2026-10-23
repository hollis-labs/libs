package search_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/hollis-labs/libs/core/store/search"
	"github.com/hollis-labs/libs/core/store/storetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPhrasePrefixOnlyPrefixesTheLastWord(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		for _, c := range []struct{ text, want string }{
			{"brown fo", "1"}, // last word is the prefix
			{"quick brown", "1"},
			{"bro fox", ""}, // "bro" is not a whole word, so this is not a prefix phrase
			{"brown f", "1"},
		} {
			got := ids(run(t, s, sr, search.Query{Match: search.Term{Text: c.text, Prefix: true}}))
			if got != c.want {
				t.Errorf("phrase prefix %q: got {%s}, want {%s}", c.text, got, c.want)
			}
		}
		// And with a field.
		got := ids(run(t, s, sr, search.Query{Match: search.Term{Text: "quick brown fo", Prefix: true, Field: "title"}}))
		if got != "1" {
			t.Errorf("field phrase prefix: {%s}", got)
		}
	})
}

func TestUpdatingTheIntegerPrimaryKeyKeepsTheIndexSound(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		storetest.Exec(t, s, `CREATE TABLE items (id INTEGER PRIMARY KEY, title TEXT NOT NULL)`)
		ix := search.Index{Name: "items_fts", Table: "items", IDColumn: "id", Fields: []search.Field{{Column: "title", Weight: 1}}}
		up, err := ix.Up(s.Dialect())
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range up {
			storetest.Exec(t, s, st)
		}
		sr, err := search.New(s.Dialect(), ix)
		if err != nil {
			t.Fatal(err)
		}
		storetest.Exec(t, s, `INSERT INTO items (id, title) VALUES (?, ?)`, 1, "walrus")
		storetest.Exec(t, s, `UPDATE items SET id = ? WHERE id = ?`, 10, 1)
		if got := ids(run(t, s, sr, search.Query{Text: "walrus"})); got != "10" {
			t.Errorf("after key change: {%s}", got)
		}
		storetest.Exec(t, s, `UPDATE items SET id = ?, title = ? WHERE id = ?`, 11, "otter", 10)
		if got := ids(run(t, s, sr, search.Query{Text: "walrus"})); got != "" {
			t.Errorf("old text still found: {%s}", got)
		}
		err = s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error { return sr.Check(ctx, tx) })
		if err != nil {
			t.Fatalf("check after key change: %v", err)
		}
	})
}

func TestTagShapedTextIsSearchable(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		storetest.Exec(t, s, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, "20", "xss", "<script>alert(1)</script> and <b>bold</b>", "a")
		for _, q := range []string{"script alert", "script", "bold", `"script alert"`} {
			if got := ids(run(t, s, sr, search.Query{Text: q})); got != "20" {
				t.Errorf("%q: {%s}", q, got)
			}
		}
		if got := ids(run(t, s, sr, search.Query{Text: "<script>"})); got != "20" {
			t.Errorf("typed with brackets: {%s}", got)
		}
	})
}

func TestSnippetIsEscapedAndMarkersAreFree(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		storetest.Exec(t, s, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, "21", "t", `see <img src=x onerror="alert(1)"> and fox & "co" 'q'`, "a")
		hits := run(t, s, sr, search.Query{Text: "fox", Filters: []search.Filter{{Column: "kind", Values: []any{"a"}}}, Snippet: &search.Snippet{Field: "body"}})
		var snip string
		for _, h := range hits {
			if h.ID == "21" {
				snip = h.Snippet
			}
		}
		if snip == "" {
			t.Fatalf("no snippet for 21 in %+v", hits)
		}
		if strings.Contains(snip, "<img") || strings.Contains(snip, `"alert`) {
			t.Errorf("stored markup reached the excerpt unescaped: %q", snip)
		}
		if !strings.Contains(snip, "<mark>fox</mark>") {
			t.Errorf("want the mark around fox: %q", snip)
		}
		// SQLite keeps the stored text, escaped; Postgres excerpts from text whose
		// angle brackets became spaces (see the README), so there is nothing to escape.
		if s.Dialect().Name() == store.DriverSQLite && !strings.Contains(snip, "&lt;img") {
			t.Errorf("want the stored markup escaped: %q", snip)
		}
		// Markers that used to be refused are fine now, and plain output is possible.
		hits = run(t, s, sr, search.Query{Text: "fox", Snippet: &search.Snippet{Field: "body", Open: `"`, Close: `"`, NoEscape: true}})
		for _, h := range hits {
			if h.ID == "21" && s.Dialect().Name() == store.DriverSQLite && !strings.Contains(h.Snippet, `<img`) {
				t.Errorf("NoEscape should leave the text as stored: %q", h.Snippet)
			}
			if h.ID == "1" && !strings.Contains(strings.ToLower(h.Snippet), `"fox"`) {
				t.Errorf("custom markers: %q", h.Snippet)
			}
		}
	})
}

func TestSubstringFoldsNonASCII(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		storetest.Exec(t, s, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, "22", "ÉCOLE normale", "Ünïcode", "b")
		for _, q := range []string{"école", "ÉCOLE", "école NORMALE"[:5], "ünï"} {
			if got := ids(run(t, s, sr, search.Query{Substring: q})); got != "22" {
				t.Errorf("substring %q: {%s}", q, got)
			}
		}
	})
}

// TestKnownEngineDifferences pins down where the engines still disagree, so a
// change in either is noticed. The README lists the same cases.
func TestKnownEngineDifferences(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		storetest.Exec(t, s, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, "23", "Straße", "Ærø and œuvre", "b")
		// Shared behaviour: simple accents fold on both.
		pg := s.Dialect().Name() == store.DriverPostgres
		for _, c := range []struct {
			q          string
			sqlite, pg string
		}{
			{"strasse", "", "23"}, // unaccent expands ß to ss; unicode61 does not
			{"Straße", "23", "23"},
			{"aero", "", "23"}, // Æ, ø
			{"oeuvre", "", "23"},
		} {
			want := c.sqlite
			if pg {
				want = c.pg
			}
			if got := ids(run(t, s, sr, search.Query{Text: c.q})); got != want {
				t.Errorf("%s %q: got {%s}, want {%s}", s.Dialect().Name(), c.q, got, want)
			}
		}
	})
}

func TestReservedWordsAreRefused(t *testing.T) {
	ix := index
	ix.Table = "order"
	if err := ix.Validate(); err == nil {
		t.Error("table named order should be refused")
	}
	ix = index
	ix.Fields = []search.Field{{Column: "group", Weight: 1}}
	if err := ix.Validate(); err == nil {
		t.Error("column named group should be refused")
	}
}

// TestIndexMigratesUnderTheRealMigrationRole creates the extensions as that
// role, in a database where they do not exist, with a search_path that does
// not start at public. The shared test harness pre-creates them as an admin,
// which hides exactly this.
func TestIndexMigratesUnderTheRealMigrationRole(t *testing.T) {
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
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	suffix := hex.EncodeToString(b)
	role, dbname := "gs_mig_"+suffix, "gs_ext_"+suffix
	for _, q := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'mig-pass'", role),
		fmt.Sprintf("CREATE DATABASE %s OWNER %s", dbname, role),
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		a := stdlib.OpenDB(*cc)
		defer a.Close()
		_, _ = a.Exec("DROP DATABASE IF EXISTS " + dbname + " WITH (FORCE)")
		_, _ = a.Exec("DROP ROLE IF EXISTS " + role)
	})
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=mig-pass dbname=%s sslmode=disable", cc.Host, cc.Port, role, dbname)
	boot, err := store.Open(ctx, store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: dsn, AllowInsecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE SCHEMA app"); err != nil {
		t.Fatal(err)
	}
	boot.Close()

	cfg := store.Config{Driver: store.DriverPostgres, Postgres: store.PostgresConfig{DSN: dsn + " search_path=app,public", AllowInsecure: true}}
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ix := search.Index{Name: "notes_fts", Table: "notes", IDColumn: "id", Fields: []search.Field{{Column: "title", Weight: 2}, {Column: "body", Weight: 1}}, Fuzzy: true}
	pgSQL, err := ix.GooseSQL(s.Dialect())
	if err != nil {
		t.Fatal(err)
	}
	lite, _ := store.DialectFor(store.DriverSQLite)
	liteSQL, _ := ix.GooseSQL(lite)
	table := "-- +goose Up\nCREATE TABLE notes (id TEXT PRIMARY KEY, title TEXT NOT NULL, body TEXT);\n\n-- +goose Down\nDROP TABLE notes;\n"
	src := migrate.Source{Root: "m", FS: fstest.MapFS{
		"m/postgres/0001_notes.sql": {Data: []byte(table)},
		"m/sqlite/0001_notes.sql":   {Data: []byte(table)},
		"m/postgres/0002_fts.sql":   {Data: []byte(pgSQL)},
		"m/sqlite/0002_fts.sql":     {Data: []byte(liteSQL)},
	}}
	if err := migrate.UpStore(ctx, s, src); err != nil {
		t.Fatalf("migrating as the owner role with search_path=app,public: %v", err)
	}
	if _, err := s.ExecContext(ctx, `INSERT INTO notes (id, title, body) VALUES (?, ?, ?)`, "1", "Café", "grand"); err != nil {
		t.Fatal(err)
	}
	sr, err := search.New(s.Dialect(), ix)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []search.Query{{Text: "cafe"}, {Substring: "caf"}} {
		hits, err := sr.Search(ctx, s.Reader(), q)
		if err != nil || len(hits) != 1 {
			t.Fatalf("%+v: %v %v", q, hits, err)
		}
	}
	if err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error { return sr.Check(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}

// SQLite documents that VACUUM may renumber the rowids of a table without an
// INTEGER PRIMARY KEY. It did not in testing here; this keeps watching, and a
// Rebuild after a VACUUM is cheap insurance either way.
func TestSearchSurvivesVacuumAndConcurrentRebuild(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		if s.Dialect().Name() == store.DriverSQLite {
			storetest.Exec(t, s, `DELETE FROM notes WHERE id IN ('2','3')`)
			storetest.Exec(t, s, `VACUUM`)
		}
		errs := make(chan error, 4)
		for i := 0; i < 4; i++ {
			go func() {
				errs <- s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error { return sr.Rebuild(ctx, tx) })
			}()
		}
		for i := 0; i < 4; i++ {
			if err := <-errs; err != nil {
				t.Errorf("rebuild: %v", err)
			}
		}
		if got := ids(run(t, s, sr, search.Query{Text: "lazy"})); got != "1,5" {
			t.Errorf("after vacuum+rebuild: {%s}", got)
		}
	})
}

func TestNonSimpleLanguageOnPostgres(t *testing.T) {
	s := storetest.Postgres(t)
	if s == nil {
		return
	}
	ix := search.Index{Name: "docs_fts", Table: "docs", IDColumn: "id", Fields: []search.Field{{Column: "title", Weight: 1}}, Language: "english"}
	storetest.Exec(t, s, `CREATE TABLE docs (id TEXT PRIMARY KEY, title TEXT NOT NULL)`)
	up, err := ix.Up(s.Dialect())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range up {
		storetest.Exec(t, s, st)
	}
	storetest.Exec(t, s, `INSERT INTO docs (id, title) VALUES (?, ?)`, "1", "Running dogs")
	sr, _ := search.New(s.Dialect(), ix)
	// Stemming is the point of a language configuration; SQLite's unicode61 has none.
	if got := ids(run(t, s, sr, search.Query{Text: "run dog"})); got != "1" {
		t.Errorf("english config should stem: {%s}", got)
	}
}

// Stored text that happens to hold private-use code points, or anything that
// looks like a marker, must not become markup.
func TestStoredTextCannotForgeMarkers(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		storetest.Exec(t, s, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, "30", "t", "head \ue000 evil \ue001 and gsOdeadbeefx fox \ue002 tail", "a")
		hits := run(t, s, sr, search.Query{Text: "fox", Filters: []search.Filter{{Column: "kind", Values: []any{"a"}}}, Snippet: &search.Snippet{Field: "body"}})
		for _, h := range hits {
			if h.ID != "30" {
				continue
			}
			if strings.Count(h.Snippet, "<mark>") != strings.Count(h.Snippet, "</mark>") || strings.Count(h.Snippet, "<mark>") != 1 {
				t.Errorf("markers not balanced or forged: %q", h.Snippet)
			}
			if strings.Contains(h.Snippet, "<mark>evil") {
				t.Errorf("stored private-use text became a marker: %q", h.Snippet)
			}
			return
		}
		t.Fatalf("no hit for 30: %+v", hits)
	})
}

func TestAngleBracketOnlyTermAgreesOnBothEngines(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		_, err := sr.Search(ctx, s.Reader(), search.Query{Match: search.Term{Text: "<"}})
		if !errors.Is(err, search.ErrInvalidQuery) {
			t.Errorf("err = %v, want ErrInvalidQuery", err)
		}
	})
}

func TestSnippetSearchesReuseOnePreparedStatementOnPostgres(t *testing.T) {
	cfg := storetest.PGConfig(t)
	if cfg == nil {
		return
	}
	cfg.Postgres.MaxConns = 1 // one session, so pg_prepared_statements sees every statement
	s, err := store.Open(ctx, *cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sr := install(t, s)
	for i := 0; i < 50; i++ {
		run(t, s, sr, search.Query{Text: "fox", Snippet: &search.Snippet{Field: "body"}})
	}
	var n int
	if err := s.QueryRowContext(ctx, `SELECT count(*) FROM pg_prepared_statements`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Every statement the test ran is in the count (the DDL and inserts too);
	// 50 distinct snippet statements would put it far above this.
	if n > 30 {
		t.Fatalf("%d prepared statements after 50 identical searches: the statement text varies per query", n)
	}
	before := n
	for i := 0; i < 50; i++ {
		run(t, s, sr, search.Query{Text: "fox", Snippet: &search.Snippet{Field: "body"}})
	}
	_ = s.QueryRowContext(ctx, `SELECT count(*) FROM pg_prepared_statements`).Scan(&n)
	if n > before+1 {
		t.Fatalf("50 more identical searches added %d prepared statements", n-before)
	}
}
