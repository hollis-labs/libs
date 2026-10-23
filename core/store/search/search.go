package search

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/hollis-labs/libs/core/store"
)

// Filter restricts results to rows whose column equals one of Values. Column
// must be listed in Index.Filters.
type Filter struct {
	Column string
	Values []any
}

// Snippet asks for a highlighted excerpt per hit.
type Snippet struct {
	// Field is the column excerpted. Default: the highest-weight field.
	Field string
	// Open and Close wrap matches. Default "<mark>" and "</mark>". The excerpt
	// is not HTML-escaped; escape it (or choose plain markers) before showing it
	// in a page.
	Open, Close string
	// Ellipsis marks omitted text. Default "…". Used on SQLite only.
	Ellipsis string
	// Words is the approximate excerpt length. Default 16.
	Words int
}

// Query is one search.
type Query struct {
	// Match is the structured query. If nil, Text is parsed.
	Match Expr
	// Text is plain user text, parsed with Parse using Parse options below.
	Text  string
	Parse ParseOptions
	// Substring, if set, replaces the full-text match with a case-insensitive
	// substring match over all fields (LIKE based; trigram-indexed on Postgres
	// when Index.Fuzzy). Hits have a constant score.
	Substring string

	Filters []Filter
	// Limit defaults to 20 and is capped at 1000.
	Limit  int
	Offset int
	// Snippet, if non-nil, fills Hit.Snippet.
	Snippet *Snippet
}

// Hit is one result.
type Hit struct {
	// ID is the IDColumn value as text.
	ID string
	// Score: higher is better. Comparable within one dialect only.
	Score   float64
	Snippet string
}

// Limits.
const (
	DefaultLimit = 20
	MaxLimit     = 1000
)

// Searcher runs queries for one Index on one dialect.
type Searcher struct {
	ix Index
	d  store.Dialect
	fs fieldSet
}

// New validates ix and binds it to a dialect.
func New(d store.Dialect, ix Index) (*Searcher, error) {
	if err := ix.Validate(); err != nil {
		return nil, err
	}
	return &Searcher{ix: ix, d: d, fs: ix.fieldSet()}, nil
}

func (s *Searcher) expr(q Query) (Expr, error) {
	e := q.Match
	if e == nil && q.Text != "" {
		po := q.Parse
		po.Fields = s.fs.names
		e = Parse(q.Text, po)
	}
	if e == nil {
		return nil, nil
	}
	return normalizeStructured(e), nil
}

// normalizeStructured leaves caller-built trees alone so mistakes surface as
// ErrInvalidQuery rather than being silently dropped.
func normalizeStructured(e Expr) Expr { return e }

func (s *Searcher) snippetField(sn *Snippet) (string, error) {
	if sn.Field == "" {
		return s.ix.Fields[s.ix.ranked()[0]].Column, nil
	}
	if !s.fs.has(sn.Field) {
		return "", fmt.Errorf("%w: unknown snippet field %q", ErrInvalidQuery, sn.Field)
	}
	return sn.Field, nil
}

func validMarker(m string) error {
	if strings.ContainsAny(m, "',\\\x00") {
		return fmt.Errorf("%w: snippet markers must not contain quotes, commas or backslashes", ErrInvalidQuery)
	}
	return nil
}

// Search runs q through qr (a Store reader or a Tx).
func (s *Searcher) Search(ctx context.Context, qr store.Querier, q Query) ([]Hit, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if q.Offset < 0 {
		return nil, fmt.Errorf("%w: negative offset", ErrInvalidQuery)
	}
	q.Substring = clean(q.Substring)
	e, err := s.expr(q)
	if err != nil {
		return nil, err
	}
	if e == nil && q.Substring == "" {
		return nil, nil // nothing to search for matches nothing
	}
	var query string
	var args []any
	switch s.d.Name() {
	case store.DriverSQLite:
		query, args, err = s.sqliteSQL(q, e)
	case store.DriverPostgres:
		query, args, err = s.postgresSQL(q, e)
	default:
		err = fmt.Errorf("%w: %q", store.ErrUnknownDriver, s.d.Name())
	}
	if err != nil {
		return nil, err
	}
	fargs, fsql, err := s.filters(q.Filters)
	if err != nil {
		return nil, err
	}
	query = strings.Replace(query, "/*FILTERS*/", fsql, 1)
	// Argument order: match args, filter args, limit, offset.
	args = append(args, fargs...)
	args = append(args, limit, q.Offset)

	rows, err := qr.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var snip sql.NullString
		if q.Snippet != nil {
			err = rows.Scan(&h.ID, &h.Score, &snip)
		} else {
			err = rows.Scan(&h.ID, &h.Score)
		}
		if err != nil {
			return nil, err
		}
		h.Snippet = snip.String
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Searcher) filters(fs []Filter) ([]any, string, error) {
	var args []any
	var b strings.Builder
	for _, f := range fs {
		ok := false
		for _, c := range s.ix.Filters {
			if c == f.Column {
				ok = true
			}
		}
		if !ok {
			return nil, "", fmt.Errorf("%w: %q is not a filter column of this index", ErrInvalidQuery, f.Column)
		}
		if len(f.Values) == 0 {
			return nil, "", fmt.Errorf("%w: filter %q has no values", ErrInvalidQuery, f.Column)
		}
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(f.Values)), ", ")
		fmt.Fprintf(&b, " AND s.%s IN (%s)", f.Column, ph)
		args = append(args, f.Values...)
	}
	return args, b.String(), nil
}

func (s *Searcher) substringWhere() (string, int) {
	parts := make([]string, len(s.ix.Fields))
	for i, f := range s.ix.Fields {
		parts[i] = "lower(s." + f.Column + ") LIKE lower(?) ESCAPE '\\'"
	}
	return "(" + strings.Join(parts, " OR ") + ")", len(parts)
}

func (s *Searcher) substringArgs(sub string, n int) []any {
	pat := "%" + store.LikeEscape(sub) + "%"
	out := make([]any, n)
	for i := range out {
		out[i] = pat
	}
	return out
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func (s *Searcher) sqliteSQL(q Query, e Expr) (string, []any, error) {
	ix := s.ix
	var sel, where string
	var args []any
	if q.Substring != "" {
		w, n := s.substringWhere()
		where, args = w, s.substringArgs(q.Substring, n)
		sel = "SELECT CAST(s." + ix.IDColumn + " AS TEXT) AS hit_id, 1.0 AS score"
		if q.Snippet != nil {
			sel += ", '' AS snip"
		}
		return sel + " FROM " + ix.Table + " s WHERE " + where + "/*FILTERS*/ ORDER BY hit_id ASC LIMIT ? OFFSET ?", args, nil
	}
	match, err := fts5Compiler(s.fs).compile(e)
	if err != nil {
		return "", nil, err
	}
	weights := make([]string, len(ix.Fields))
	for i, f := range ix.Fields {
		weights[i] = num(f.Weight)
	}
	sel = fmt.Sprintf("SELECT CAST(s.%s AS TEXT) AS hit_id, -bm25(%s, %s) AS score", ix.IDColumn, ix.Name, strings.Join(weights, ", "))
	if q.Snippet != nil {
		sn := *q.Snippet
		field, err := s.snippetField(&sn)
		if err != nil {
			return "", nil, err
		}
		open, cl, ell, words := defaults(sn)
		for _, m := range []string{open, cl, ell} {
			if err := validMarker(m); err != nil {
				return "", nil, err
			}
		}
		col := 0
		for i, f := range ix.Fields {
			if f.Column == field {
				col = i
			}
		}
		sel += fmt.Sprintf(", snippet(%s, %d, '%s', '%s', '%s', %d) AS snip", ix.Name, col, open, cl, ell, words)
	}
	return sel + fmt.Sprintf(" FROM %s JOIN %s s ON s.rowid = %s.rowid WHERE %s MATCH ?/*FILTERS*/ ORDER BY score DESC, hit_id ASC LIMIT ? OFFSET ?",
		ix.Name, ix.Table, ix.Name, ix.Name), []any{match}, nil
}

func defaults(sn Snippet) (open, cl, ell string, words int) {
	open, cl, ell, words = sn.Open, sn.Close, sn.Ellipsis, sn.Words
	if open == "" {
		open = "<mark>"
	}
	if cl == "" {
		cl = "</mark>"
	}
	if ell == "" {
		ell = "…"
	}
	if words <= 0 {
		words = 16
	}
	if words > 64 {
		words = 64 // FTS5 snippet() limit
	}
	return
}

func (s *Searcher) postgresSQL(q Query, e Expr) (string, []any, error) {
	ix := s.ix
	if q.Substring != "" {
		w, n := s.substringWhere()
		sel := "SELECT s." + ix.IDColumn + "::text AS hit_id, 1.0::float8 AS score"
		if q.Snippet != nil {
			sel += ", ''::text AS snip"
		}
		return sel + " FROM " + ix.Table + " s WHERE " + w + "/*FILTERS*/ ORDER BY hit_id ASC LIMIT ? OFFSET ?", s.substringArgs(q.Substring, n), nil
	}
	tsq, err := tsCompiler(s.fs).compile(e)
	if err != nil {
		return "", nil, err
	}
	// ts_rank_cd takes weights for classes D, C, B, A.
	ranked := ix.ranked()
	maxW := ix.Fields[ranked[0]].Weight
	w := [4]float64{0.05, 0.05, 0.05, 0.05}
	for rank, i := range ranked {
		w[3-rank] = ix.Fields[i].Weight / maxW
	}
	warr := fmt.Sprintf("'{%s,%s,%s,%s}'::float4[]", num(w[0]), num(w[1]), num(w[2]), num(w[3]))
	qexpr := "to_tsquery('" + ix.language() + "', "
	arg := tsq
	if ix.KeepDiacritics {
		qexpr += "?)"
	} else {
		qexpr += "gostore_unaccent(?))"
	}
	sel := fmt.Sprintf("SELECT s.%s::text AS hit_id, ts_rank_cd(%s, s.%s, q.q, 1)::float8 AS score", ix.IDColumn, warr, ix.vecColumn())
	if q.Snippet != nil {
		sn := *q.Snippet
		field, err := s.snippetField(&sn)
		if err != nil {
			return "", nil, err
		}
		open, cl, _, words := defaults(sn)
		for _, m := range []string{open, cl} {
			if err := validMarker(m); err != nil {
				return "", nil, err
			}
		}
		doc := "coalesce(s." + field + ", '')"
		if !ix.KeepDiacritics {
			doc = "gostore_unaccent(" + doc + ")"
		}
		minW := words / 2
		if minW < 1 {
			minW = 1
		}
		sel += fmt.Sprintf(", ts_headline('%s', %s, q.q, 'StartSel=%s, StopSel=%s, MaxWords=%d, MinWords=%d, MaxFragments=1') AS snip",
			ix.language(), doc, open, cl, words, minW)
	}
	return sel + fmt.Sprintf(" FROM %s s, (SELECT %s AS q) q WHERE s.%s @@ q.q/*FILTERS*/ ORDER BY score DESC, hit_id ASC LIMIT ? OFFSET ?",
		ix.Table, qexpr, ix.vecColumn()), []any{arg}, nil
}

// Rebuild recreates the index from the source table. On SQLite this is the FTS5
// 'rebuild' command (needed after bulk loads that bypassed the triggers, or a
// VACUUM that renumbered rowids); on Postgres the tsvector column is generated
// and always current, so this reindexes the GIN index.
func (s *Searcher) Rebuild(ctx context.Context, qr store.Querier) error {
	switch s.d.Name() {
	case store.DriverSQLite:
		_, err := qr.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(%s) VALUES ('rebuild')", s.ix.Name, s.ix.Name))
		return err
	case store.DriverPostgres:
		_, err := qr.ExecContext(ctx, "REINDEX INDEX "+s.ix.Name+"_gin")
		return err
	}
	return fmt.Errorf("%w: %q", store.ErrUnknownDriver, s.d.Name())
}

// Check verifies the index against the source table. On SQLite it runs the FTS5
// integrity-check with content comparison; on Postgres it confirms no row has a
// missing vector. A non-nil error means the index is damaged: Rebuild it.
func (s *Searcher) Check(ctx context.Context, qr store.Querier) error {
	switch s.d.Name() {
	case store.DriverSQLite:
		_, err := qr.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(%s, rank) VALUES ('integrity-check', 1)", s.ix.Name, s.ix.Name))
		if err != nil {
			return fmt.Errorf("search: integrity check failed: %w", err)
		}
		return nil
	case store.DriverPostgres:
		var n int64
		if err := qr.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NULL", s.ix.Table, s.ix.vecColumn())).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("search: %d rows have no search vector", n)
		}
		return nil
	}
	return fmt.Errorf("%w: %q", store.ErrUnknownDriver, s.d.Name())
}
