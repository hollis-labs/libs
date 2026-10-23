package search

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hollis-labs/libs/core/store"
)

// Field is one searchable text column of the source table.
type Field struct {
	Column string
	// Weight scales the field's contribution to the score; larger is more
	// important. Must be positive. On Postgres weights map to the four
	// setweight classes A-D by rank, so at most four fields are supported.
	Weight float64
}

// Index defines one searchable index over a source table.
type Index struct {
	// Name is the base name of the derived objects: the FTS5 table and trigger
	// prefix on SQLite, the tsvector column ("<Name>_vec") and index names on
	// Postgres.
	Name string
	// Table is the source table holding the text. On SQLite it must have a
	// rowid (not WITHOUT ROWID). Rowids of a table without an INTEGER PRIMARY
	// KEY can change on VACUUM; run [Searcher.Rebuild] after one.
	Table string
	// IDColumn is the source column returned as Hit.ID (any type; returned as
	// text).
	IDColumn string
	// Fields are the indexed columns, one to four.
	Fields []Field
	// Filters are source columns a query may filter on by equality or IN.
	Filters []string

	// Tokenizer is the FTS5 tokenize string (SQLite). Default
	// "unicode61 remove_diacritics 2" ("... remove_diacritics 0" when
	// KeepDiacritics).
	Tokenizer string
	// PrefixLengths adds FTS5 prefix indexes (for example 2, 3) so prefix terms
	// are fast (SQLite).
	PrefixLengths []int
	// Language is the Postgres text-search configuration. Default "simple".
	Language string
	// KeepDiacritics turns off accent folding (unaccent on Postgres).
	KeepDiacritics bool
	// UnaccentSchema is the schema holding the unaccent extension on Postgres.
	// Default "public".
	UnaccentSchema string
	// Fuzzy adds pg_trgm GIN indexes on lower(field) so [Query.Substring] is
	// indexed on Postgres.
	Fuzzy bool
	// ImmutableContent omits the UPDATE trigger on SQLite for tables whose
	// indexed columns never change after insert.
	ImmutableContent bool
}

var (
	tokenizerRe = regexp.MustCompile(`^[A-Za-z0-9_ =.\-]+$`)
	langRe      = regexp.MustCompile(`^[a-z_]+$`)
	letters     = []byte{'A', 'B', 'C', 'D'}
)

// Validate checks the definition. Names are spliced into DDL and queries, so
// they must be plain identifiers.
func (ix Index) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalidIndex}, a...)...)
	}
	for _, n := range []string{ix.Name, ix.Table, ix.IDColumn} {
		if err := store.CheckIdentifier(n); err != nil || strings.Contains(n, ".") {
			return bad("%q is not a plain identifier", n)
		}
	}
	if ix.Name == ix.Table {
		return bad("name must differ from the source table")
	}
	if len(ix.Fields) == 0 || len(ix.Fields) > 4 {
		return bad("need 1 to 4 fields, have %d", len(ix.Fields))
	}
	seen := map[string]bool{}
	for _, f := range ix.Fields {
		if err := store.CheckIdentifier(f.Column); err != nil || strings.Contains(f.Column, ".") {
			return bad("field %q is not a plain identifier", f.Column)
		}
		if seen[f.Column] {
			return bad("duplicate field %q", f.Column)
		}
		seen[f.Column] = true
		if f.Weight <= 0 || math.IsNaN(f.Weight) || math.IsInf(f.Weight, 0) {
			return bad("field %q needs a positive weight", f.Column)
		}
	}
	for _, c := range ix.Filters {
		if err := store.CheckIdentifier(c); err != nil || strings.Contains(c, ".") {
			return bad("filter %q is not a plain identifier", c)
		}
	}
	if ix.Tokenizer != "" && !tokenizerRe.MatchString(ix.Tokenizer) {
		return bad("tokenizer %q has unsupported characters", ix.Tokenizer)
	}
	if ix.Language != "" && !langRe.MatchString(ix.Language) {
		return bad("language %q", ix.Language)
	}
	if ix.UnaccentSchema != "" {
		if err := store.CheckIdentifier(ix.UnaccentSchema); err != nil || strings.Contains(ix.UnaccentSchema, ".") {
			return bad("unaccent schema %q", ix.UnaccentSchema)
		}
	}
	for _, n := range ix.PrefixLengths {
		if n < 1 || n > 10 {
			return bad("prefix length %d out of range", n)
		}
	}
	return nil
}

func (ix Index) language() string {
	if ix.Language == "" {
		return "simple"
	}
	return ix.Language
}

func (ix Index) unaccentSchema() string {
	if ix.UnaccentSchema == "" {
		return "public"
	}
	return ix.UnaccentSchema
}

func (ix Index) tokenizer() string {
	switch {
	case ix.Tokenizer != "":
		return ix.Tokenizer
	case ix.KeepDiacritics:
		return "unicode61 remove_diacritics 0"
	}
	return "unicode61 remove_diacritics 2"
}

// ranked returns field indexes ordered by weight descending, then declaration
// order; the position is the Postgres weight class.
func (ix Index) ranked() []int {
	idx := make([]int, len(ix.Fields))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return ix.Fields[idx[a]].Weight > ix.Fields[idx[b]].Weight })
	return idx
}

func (ix Index) fieldSet() fieldSet {
	fs := fieldSet{letters: map[string]byte{}}
	for _, f := range ix.Fields {
		fs.names = append(fs.names, f.Column)
	}
	for rank, i := range ix.ranked() {
		fs.letters[ix.Fields[i].Column] = letters[rank]
	}
	return fs
}

func (ix Index) vecColumn() string { return ix.Name + "_vec" }

func (ix Index) cols() string {
	c := make([]string, len(ix.Fields))
	for i, f := range ix.Fields {
		c[i] = f.Column
	}
	return strings.Join(c, ", ")
}

func (ix Index) prefixed(p string) string {
	c := make([]string, len(ix.Fields))
	for i, f := range ix.Fields {
		c[i] = p + f.Column
	}
	return strings.Join(c, ", ")
}

// Up returns the statements that create the index and backfill it from the
// existing rows, for a migration's Up section.
func (ix Index) Up(d store.Dialect) ([]string, error) {
	if err := ix.Validate(); err != nil {
		return nil, err
	}
	switch d.Name() {
	case store.DriverSQLite:
		return ix.sqliteUp(), nil
	case store.DriverPostgres:
		return ix.postgresUp(), nil
	}
	return nil, fmt.Errorf("%w: %q", store.ErrUnknownDriver, d.Name())
}

// Down returns the statements that drop what Up created.
func (ix Index) Down(d store.Dialect) ([]string, error) {
	if err := ix.Validate(); err != nil {
		return nil, err
	}
	switch d.Name() {
	case store.DriverSQLite:
		out := []string{
			"DROP TRIGGER IF EXISTS " + ix.Name + "_ai",
			"DROP TRIGGER IF EXISTS " + ix.Name + "_ad",
			"DROP TRIGGER IF EXISTS " + ix.Name + "_au",
			"DROP TABLE IF EXISTS " + ix.Name,
		}
		return out, nil
	case store.DriverPostgres:
		out := []string{"DROP INDEX IF EXISTS " + ix.Name + "_gin"}
		for _, f := range ix.Fields {
			out = append(out, "DROP INDEX IF EXISTS "+ix.Name+"_trgm_"+f.Column)
		}
		out = append(out, "ALTER TABLE "+ix.Table+" DROP COLUMN IF EXISTS "+ix.vecColumn())
		return out, nil
	}
	return nil, fmt.Errorf("%w: %q", store.ErrUnknownDriver, d.Name())
}

func (ix Index) sqliteUp() []string {
	opts := []string{
		"content='" + ix.Table + "'",
		"content_rowid='rowid'",
		"tokenize='" + ix.tokenizer() + "'",
	}
	if len(ix.PrefixLengths) > 0 {
		p := make([]string, len(ix.PrefixLengths))
		for i, n := range ix.PrefixLengths {
			p[i] = strconv.Itoa(n)
		}
		opts = append(opts, "prefix='"+strings.Join(p, " ")+"'")
	}
	out := []string{
		fmt.Sprintf("CREATE VIRTUAL TABLE %s USING fts5(%s, %s)", ix.Name, ix.cols(), strings.Join(opts, ", ")),
		fmt.Sprintf("CREATE TRIGGER %s_ai AFTER INSERT ON %s BEGIN\n  INSERT INTO %s(rowid, %s) VALUES (new.rowid, %s);\nEND",
			ix.Name, ix.Table, ix.Name, ix.cols(), ix.prefixed("new.")),
		fmt.Sprintf("CREATE TRIGGER %s_ad AFTER DELETE ON %s BEGIN\n  INSERT INTO %s(%s, rowid, %s) VALUES ('delete', old.rowid, %s);\nEND",
			ix.Name, ix.Table, ix.Name, ix.Name, ix.cols(), ix.prefixed("old.")),
	}
	if !ix.ImmutableContent {
		out = append(out, fmt.Sprintf("CREATE TRIGGER %s_au AFTER UPDATE OF %s ON %s BEGIN\n  INSERT INTO %s(%s, rowid, %s) VALUES ('delete', old.rowid, %s);\n  INSERT INTO %s(rowid, %s) VALUES (new.rowid, %s);\nEND",
			ix.Name, ix.cols(), ix.Table, ix.Name, ix.Name, ix.cols(), ix.prefixed("old."), ix.Name, ix.cols(), ix.prefixed("new.")))
	}
	return append(out, fmt.Sprintf("INSERT INTO %s(%s) VALUES ('rebuild')", ix.Name, ix.Name))
}

func (ix Index) postgresUp() []string {
	var out []string
	fold := !ix.KeepDiacritics
	wrap := func(col string) string {
		e := "coalesce(" + col + ", '')"
		if fold {
			e = "gostore_unaccent(" + e + ")"
		}
		return e
	}
	if fold {
		us := ix.unaccentSchema()
		out = append(out,
			"CREATE EXTENSION IF NOT EXISTS unaccent",
			// gostore:allow dollar-placeholder (a Postgres function body, emitted into a migration, never run through Rebind)
			fmt.Sprintf("CREATE OR REPLACE FUNCTION gostore_unaccent(text) RETURNS text\n  LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT\n  AS $$ SELECT %s.unaccent('%s.unaccent'::regdictionary, $1) $$", us, us))
	}
	if ix.Fuzzy {
		out = append(out, "CREATE EXTENSION IF NOT EXISTS pg_trgm")
	}
	fs := ix.fieldSet()
	parts := make([]string, len(ix.Fields))
	for i, f := range ix.Fields {
		parts[i] = fmt.Sprintf("setweight(to_tsvector('%s', %s), '%c')", ix.language(), wrap(f.Column), fs.letters[f.Column])
	}
	out = append(out,
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s tsvector GENERATED ALWAYS AS (\n  %s\n) STORED", ix.Table, ix.vecColumn(), strings.Join(parts, " ||\n  ")),
		fmt.Sprintf("CREATE INDEX %s_gin ON %s USING GIN (%s)", ix.Name, ix.Table, ix.vecColumn()))
	if ix.Fuzzy {
		for _, f := range ix.Fields {
			out = append(out, fmt.Sprintf("CREATE INDEX %s_trgm_%s ON %s USING GIN (lower(%s) gin_trgm_ops)", ix.Name, f.Column, ix.Table, f.Column))
		}
	}
	return out
}

// GooseSQL renders a complete goose migration body for a dialect: the Up and
// Down sections, each statement wrapped in StatementBegin/End so triggers and
// functions survive goose's statement splitter.
func (ix Index) GooseSQL(d store.Dialect) (string, error) {
	up, err := ix.Up(d)
	if err != nil {
		return "", err
	}
	down, err := ix.Down(d)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	section := func(name string, stmts []string) {
		b.WriteString("-- +goose " + name + "\n")
		for _, s := range stmts {
			b.WriteString("-- +goose StatementBegin\n" + s + ";\n-- +goose StatementEnd\n")
		}
	}
	section("Up", up)
	b.WriteString("\n")
	section("Down", down)
	return b.String(), nil
}
