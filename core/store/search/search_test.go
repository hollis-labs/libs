package search_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/hollis-labs/libs/core/store/search"
	"github.com/hollis-labs/libs/core/store/storetest"
)

var ctx = context.Background()

var index = search.Index{
	Name:     "notes_fts",
	Table:    "notes",
	IDColumn: "id",
	Fields:   []search.Field{{Column: "title", Weight: 5}, {Column: "body", Weight: 1}},
	Filters:  []string{"kind"},
	Fuzzy:    true,
}

type doc struct{ id, title, body, kind string }

var corpus = []doc{
	{"1", "Quick brown fox", "The quick brown fox jumps over the lazy dog", "a"},
	{"2", "Café culture", "A café in Paris serves crème brûlée and strong coffee", "b"},
	{"3", "Database indexing", "B-tree indexes speed up lookups; full-text search uses inverted indexes", "a"},
	{"4", "Postgres and SQLite", "Portable SQL runs on both Postgres and SQLite engines", "b"},
	{"5", "Lazy afternoon", "A lazy cat sleeps all afternoon; the dog barks", "a"},
	{"6", "Résumé tips", "Write a clear resume and keep the layout simple", "b"},
	{"7", "Operators", "SELECT * FROM users WHERE name = 'x'; DROP TABLE users;--", "a"},
	{"8", "100% natural_blend", "pure", "b"},
}

// install creates the source table, the index and the corpus.
func install(t *testing.T, s *store.Store) *search.Searcher {
	t.Helper()
	storetest.Exec(t, s, `CREATE TABLE notes (id TEXT PRIMARY KEY, title TEXT NOT NULL, body TEXT, kind TEXT NOT NULL)`)
	up, err := index.Up(s.Dialect())
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range up {
		storetest.Exec(t, s, stmt)
	}
	for _, d := range corpus {
		storetest.Exec(t, s, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, d.id, d.title, d.body, d.kind)
	}
	// A row with NULL body, inserted after the index exists.
	storetest.Exec(t, s, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, "9", "Nothing here", nil, "a")
	sr, err := search.New(s.Dialect(), index)
	if err != nil {
		t.Fatal(err)
	}
	return sr
}

func ids(hits []search.Hit) string {
	var out []string
	for _, h := range hits {
		out = append(out, h.ID)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func run(t *testing.T, s *store.Store, sr *search.Searcher, q search.Query) []search.Hit {
	t.Helper()
	hits, err := sr.Search(ctx, s.Reader(), q)
	if err != nil {
		t.Fatalf("search %+v: %v", q, err)
	}
	return hits
}

func TestSetsMatchOnBothDialects(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		text := func(q string, o ...func(*search.Query)) search.Query {
			qq := search.Query{Text: q}
			for _, f := range o {
				f(&qq)
			}
			return qq
		}
		anyMode := func(q *search.Query) { q.Parse.Any = true }
		cases := []struct {
			name string
			q    search.Query
			want string
		}{
			{"word", text("fox"), "1"},
			{"case", text("FOX"), "1"},
			{"and", text("lazy dog"), "1,5"},
			{"and narrows", text("lazy cat"), "5"},
			{"phrase", text(`"lazy dog"`), "1"},
			{"phrase non adjacent", text(`"lazy cat dog"`), ""},
			{"any", text("fox cat", anyMode), "1,5"},
			{"or", text("fox OR postgres"), "1,4"},
			{"not", text("lazy NOT cat"), "1"},
			{"minus", text("-cat lazy"), "1"},
			{"group", text("(fox OR cat) dog"), "1,5"},
			{"prefix", text("ind*"), "3"},
			{"typeahead", search.Query{Text: "laz", Parse: search.ParseOptions{PrefixLast: true}}, "1,5"},
			{"diacritic query", text("café"), "2"},
			{"diacritic folded", text("cafe"), "2"},
			{"diacritic body", text("creme brulee"), "2"},
			{"diacritic title", text("resume"), "6"},
			{"diacritic accented", text("résumé"), "6"},
			{"field title", text("title:fox"), "1"},
			{"field miss", text("title:dog"), ""},
			{"field body", text("body:dog"), "1,5"},
			{"null body row by title", text("nothing"), "9"},
			{"filter kind a", search.Query{Text: "lazy", Filters: []search.Filter{{Column: "kind", Values: []any{"a"}}}}, "1,5"},
			{"filter kind b", search.Query{Text: "lazy", Filters: []search.Filter{{Column: "kind", Values: []any{"b"}}}}, ""},
			{"filter in", search.Query{Text: "café OR lazy", Filters: []search.Filter{{Column: "kind", Values: []any{"b", "x"}}}}, "2"},
			{"structured", search.Query{Match: search.And{search.Term{Text: "lazy"}, search.Not{X: search.Term{Text: "cat"}}}}, "1"},
			{"structured field prefix", search.Query{Match: search.Term{Text: "ind", Prefix: true, Field: "title"}}, "3"},
			{"sql words are data", text("DROP TABLE users"), "7"},
			{"select word", text("select"), "7"},
			{"substring", search.Query{Substring: "rown"}, "1"},
			{"substring case", search.Query{Substring: "BROWN"}, "1"},
			{"substring percent is literal", search.Query{Substring: "100%"}, "8"},
			{"substring underscore is literal", search.Query{Substring: "l_b"}, "8"},
			{"substring none", search.Query{Substring: "zzzz"}, ""},
		}
		for _, c := range cases {
			c := c
			t.Run(c.name, func(t *testing.T) {
				if got := ids(run(t, s, sr, c.q)); got != c.want {
					t.Errorf("got {%s}, want {%s}", got, c.want)
				}
			})
		}
	})
}

func TestHostileAndEmptyInputNeverErrorsOrInjects(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		for _, in := range []string{
			"", " ", `"`, `""`, `'`, `''`, `)(`, "(", ")", "*", "**", "-", "--", ":", "title:", ":x",
			"AND", "OR", "NOT", "NEAR(", "NEAR(a b)", "a OR", "OR a", "a AND AND b", "NOT NOT a",
			`"unterminated`, `'; DROP TABLE notes;--`, `") OR 1=1 --`, `\`, `\'`, `a\`, "a'b", `a"b`,
			"\x00", "fox\x00dog", strings.Repeat("a ", 500), strings.Repeat("(", 100) + "a", "𝒳 日本語 العربية",
			"title:fox OR body:", "{title}:fox", "^fox", "fox*", "col:x", "!&|:*<->", "a & b | !c",
		} {
			for _, mode := range []bool{false, true} {
				_, err := sr.Search(ctx, s.Reader(), search.Query{Text: in, Parse: search.ParseOptions{Any: mode, PrefixLast: mode}})
				if err != nil {
					t.Errorf("Text %q (any=%v): %v", in, mode, err)
				}
			}
			if _, err := sr.Search(ctx, s.Reader(), search.Query{Substring: in}); err != nil {
				t.Errorf("Substring %q: %v", in, err)
			}
			// Even as a literal term in a hand-built tree: escaped, not interpreted.
			if strings.TrimSpace(in) != "" {
				_, err := sr.Search(ctx, s.Reader(), search.Query{Match: search.Term{Text: in}})
				if err != nil && !(strings.Trim(in, "\x00") == "" && errors.Is(err, search.ErrInvalidQuery)) {
					t.Errorf("Term %q: %v", in, err)
				}
				_, err = sr.Search(ctx, s.Reader(), search.Query{Match: search.Term{Text: in, Prefix: true, Field: "body"}})
				if err != nil && !(strings.Trim(in, "\x00") == "" && errors.Is(err, search.ErrInvalidQuery)) {
					t.Errorf("prefix Term %q: %v", in, err)
				}
			}
		}
		var n int
		if err := s.QueryRowContext(ctx, `SELECT count(*) FROM notes`).Scan(&n); err != nil || n != len(corpus)+1 {
			t.Fatalf("table damaged: n=%d err=%v", n, err)
		}
	})
}

func TestInvalidStructuredQueriesAreErrors(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		for name, q := range map[string]search.Query{
			"standalone not":  {Match: search.Not{X: search.Term{Text: "a"}}},
			"only negatives":  {Match: search.And{search.Not{X: search.Term{Text: "a"}}}},
			"unknown field":   {Match: search.Term{Text: "a", Field: "nope"}},
			"empty term":      {Match: search.Term{Text: "  "}},
			"empty or":        {Match: search.Or{}},
			"bad filter col":  {Text: "fox", Filters: []search.Filter{{Column: "title", Values: []any{"x"}}}},
			"empty filter":    {Text: "fox", Filters: []search.Filter{{Column: "kind"}}},
			"negative offset": {Text: "fox", Offset: -1},
			"bad snippet":     {Text: "fox", Snippet: &search.Snippet{Field: "nope"}},
			"quote marker":    {Text: "fox", Snippet: &search.Snippet{Open: "'"}},
		} {
			if _, err := sr.Search(ctx, s.Reader(), q); !errors.Is(err, search.ErrInvalidQuery) {
				t.Errorf("%s: err = %v, want ErrInvalidQuery", name, err)
			}
		}
	})
}

func TestRankingTitleOutweighsBody(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		hits := run(t, s, sr, search.Query{Text: "lazy"})
		if len(hits) != 2 || hits[0].ID != "5" {
			t.Fatalf("hits = %+v, want doc 5 (title match) first", hits)
		}
		if !(hits[0].Score > hits[1].Score) {
			t.Errorf("scores must be higher-is-better: %+v", hits)
		}
	})
}

func TestSnippet(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		hits := run(t, s, sr, search.Query{Text: "fox", Snippet: &search.Snippet{Field: "body"}})
		if len(hits) != 1 || !strings.Contains(strings.ToLower(hits[0].Snippet), "<mark>fox</mark>") {
			t.Fatalf("snippet = %+v", hits)
		}
		hits = run(t, s, sr, search.Query{Text: "lazy", Snippet: &search.Snippet{Open: "[", Close: "]", Words: 5}})
		if len(hits) == 0 || !strings.Contains(strings.ToLower(hits[0].Snippet), "[lazy]") {
			t.Fatalf("custom marker snippet = %+v", hits)
		}
		hits = run(t, s, sr, search.Query{Substring: "rown", Snippet: &search.Snippet{}})
		if len(hits) != 1 {
			t.Fatalf("substring with snippet: %+v", hits)
		}
	})
}

func TestPagingIsStableAndComplete(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		q := search.Query{Text: "lazy OR fox OR dog OR postgres OR cat"}
		full := run(t, s, sr, q)
		var paged []search.Hit
		for off := 0; ; off++ {
			q.Limit, q.Offset = 1, off
			page := run(t, s, sr, q)
			if len(page) == 0 {
				break
			}
			paged = append(paged, page...)
		}
		if ids(paged) != ids(full) || len(paged) != len(full) {
			t.Fatalf("paged %v != full %v", paged, full)
		}
		for i := range full {
			if paged[i].ID != full[i].ID {
				t.Fatalf("order differs at %d: %v vs %v", i, paged, full)
			}
		}
		q.Limit, q.Offset = 100000, 0
		if got := len(run(t, s, sr, q)); got != len(full) {
			t.Errorf("capped limit returned %d", got)
		}
	})
}

func TestIndexStaysInSyncWithTheTable(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		storetest.Exec(t, s, `UPDATE notes SET title = ?, body = ? WHERE id = ?`, "Quick red wolf", "howls at the moon", "1")
		if got := ids(run(t, s, sr, search.Query{Text: "fox"})); got != "" {
			t.Errorf("updated-away term still found: {%s}", got)
		}
		if got := ids(run(t, s, sr, search.Query{Text: "wolf moon"})); got != "1" {
			t.Errorf("updated term not found: {%s}", got)
		}
		storetest.Exec(t, s, `DELETE FROM notes WHERE id = ?`, "5")
		if got := ids(run(t, s, sr, search.Query{Text: "lazy"})); got != "" {
			t.Errorf("deleted row still found: {%s}", got)
		}
		if err := sr.Check(ctx, s.Reader()); err != nil {
			t.Errorf("check: %v", err)
		}
		// Check and Rebuild run through a transaction on the write connection.
		err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			if err := sr.Rebuild(ctx, tx); err != nil {
				return err
			}
			return sr.Check(ctx, tx)
		})
		if err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		if got := ids(run(t, s, sr, search.Query{Text: "wolf"})); got != "1" {
			t.Errorf("after rebuild: {%s}", got)
		}
	})
}

func TestSearchInsideATransactionSeesItsOwnWrites(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, "t1", "Zebra crossing", "stripes", "a"); err != nil {
				return err
			}
			hits, err := sr.Search(ctx, tx, search.Query{Text: "zebra"})
			if err != nil {
				return err
			}
			if ids(hits) != "t1" {
				return fmt.Errorf("tx search = {%s}", ids(hits))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestDownThenUpCycle(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sr := install(t, s)
		down, err := index.Down(s.Dialect())
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range down {
			storetest.Exec(t, s, stmt)
		}
		up, _ := index.Up(s.Dialect())
		for _, stmt := range up {
			storetest.Exec(t, s, stmt)
		}
		if got := ids(run(t, s, sr, search.Query{Text: "fox"})); got != "1" {
			t.Errorf("after down/up: {%s}", got)
		}
	})
}

func TestGooseSQLIsAValidMigration(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		sql, err := index.GooseSQL(s.Dialect())
		if err != nil {
			t.Fatal(err)
		}
		table := "-- +goose Up\nCREATE TABLE notes (id TEXT PRIMARY KEY, title TEXT NOT NULL, body TEXT, kind TEXT NOT NULL);\n-- +goose Down\nDROP TABLE notes;\n"
		files := fstest.MapFS{}
		for _, d := range []string{"sqlite", "postgres"} {
			files[d+"/0001_notes.sql"] = &fstest.MapFile{Data: []byte(table)}
			dd, _ := store.DialectFor(store.Driver(d))
			body, err := index.GooseSQL(dd)
			if err != nil {
				t.Fatal(err)
			}
			files[d+"/0002_notes_fts.sql"] = &fstest.MapFile{Data: []byte(body)}
		}
		_ = sql
		src := migrate.Source{FS: files}
		if err := migrate.CheckParity(src); err != nil {
			t.Fatal(err)
		}
		if err := migrate.UpStore(ctx, s, src); err != nil {
			t.Fatalf("goose up: %v", err)
		}
		storetest.Exec(t, s, `INSERT INTO notes (id, title, body, kind) VALUES (?, ?, ?, ?)`, "1", "Goose made me", "searchable", "a")
		sr, _ := search.New(s.Dialect(), index)
		if got := ids(run(t, s, sr, search.Query{Text: "searchable"})); got != "1" {
			t.Errorf("search after goose migration: {%s}", got)
		}
	})
}

func TestIndexValidation(t *testing.T) {
	good := index
	bad := map[string]func(*search.Index){
		"name":         func(i *search.Index) { i.Name = "x; drop" },
		"same name":    func(i *search.Index) { i.Name = i.Table },
		"no fields":    func(i *search.Index) { i.Fields = nil },
		"five fields":  func(i *search.Index) { i.Fields = make([]search.Field, 5) },
		"zero weight":  func(i *search.Index) { i.Fields = []search.Field{{Column: "a", Weight: 0}} },
		"dup field":    func(i *search.Index) { i.Fields = []search.Field{{Column: "a", Weight: 1}, {Column: "a", Weight: 1}} },
		"qualified":    func(i *search.Index) { i.IDColumn = "t.id" },
		"tokenizer":    func(i *search.Index) { i.Tokenizer = "unicode61'); DROP TABLE x;--" },
		"filter":       func(i *search.Index) { i.Filters = []string{"a b"} },
		"language":     func(i *search.Index) { i.Language = "simple'; --" },
		"prefix range": func(i *search.Index) { i.PrefixLengths = []int{0} },
	}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range bad {
		ix := good
		ix.Fields = append([]search.Field(nil), good.Fields...)
		mutate(&ix)
		if err := ix.Validate(); !errors.Is(err, search.ErrInvalidIndex) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDDLShapes(t *testing.T) {
	lite, _ := store.DialectFor(store.DriverSQLite)
	pg, _ := store.DialectFor(store.DriverPostgres)
	ix := index
	ix.PrefixLengths = []int{2, 3}
	up, _ := ix.Up(lite)
	joined := strings.Join(up, "\n")
	for _, want := range []string{"fts5(title, body", "content='notes'", "remove_diacritics 2", "prefix='2 3'", "notes_fts_ai", "notes_fts_ad", "notes_fts_au", "'rebuild'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("sqlite DDL missing %q:\n%s", want, joined)
		}
	}
	ix.ImmutableContent = true
	up, _ = ix.Up(lite)
	if strings.Contains(strings.Join(up, "\n"), "_au") {
		t.Error("immutable content should omit the update trigger")
	}
	up, _ = index.Up(pg)
	joined = strings.Join(up, "\n")
	for _, want := range []string{"unaccent", "GENERATED ALWAYS AS", "setweight(to_tsvector('simple'", "'A')", "'B')", "USING GIN (notes_fts_vec)", "gin_trgm_ops", "pg_trgm"} {
		if !strings.Contains(joined, want) {
			t.Errorf("postgres DDL missing %q:\n%s", want, joined)
		}
	}
}
