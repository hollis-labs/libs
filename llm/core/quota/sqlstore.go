package quota

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Querier is the part of *sql.DB (and *sql.Tx, *sql.Conn) SQLStore uses.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// SQLStore reads usage from a table of the application's own events through
// database/sql. It never opens a connection, never creates or alters a table
// and never writes or deletes a row: the application owns the database, the
// table and its retention. It is not a Recorder; the application's event row
// is the record (see Recorder).
//
// The table needs one row per usage event with a timestamp column and an
// amount column. Filter maps a Counter to the WHERE clause that selects its
// rows; the window part of the counter is usually ignored there, because an
// application's events are not split by window.
type SQLStore struct {
	// DB runs the queries.
	DB Querier
	// Table, TimeColumn and AmountColumn name the events table and its columns.
	// They are written into the SQL as given and must be identifiers
	// (letters, digits, underscore, optionally schema-qualified with a dot);
	// anything else is refused.
	Table        string
	TimeColumn   string
	AmountColumn string
	// Filter returns the WHERE condition (without "WHERE") that selects the
	// rows of c, using "?" for every parameter, and its arguments.
	Filter func(c Counter) (cond string, args []any)
	// Placeholder renders the n-th parameter (1-based) for the driver; nil
	// keeps "?" (SQLite, MySQL). Use DollarPlaceholder for PostgreSQL.
	Placeholder func(n int) string
	// TimeArg converts a bound for the driver; nil passes t.UTC().
	TimeArg func(t time.Time) any
	// ScanTime converts a scanned timestamp; nil accepts time.Time, RFC 3339
	// strings (with or without fractional seconds) and their []byte form.
	ScanTime func(src any) (time.Time, error)
}

// DollarPlaceholder renders $1, $2, ... for PostgreSQL drivers.
func DollarPlaceholder(n int) string { return "$" + strconv.Itoa(n) }

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

func (s *SQLStore) query(c Counter, sel string, from, to time.Time) (string, []any, error) {
	if s.DB == nil {
		return "", nil, errors.New("quota: SQLStore has no DB")
	}
	for _, id := range []string{s.Table, s.TimeColumn, s.AmountColumn} {
		if !identRE.MatchString(id) {
			return "", nil, fmt.Errorf("quota: SQLStore: %q is not an identifier", id)
		}
	}
	if s.Filter == nil {
		return "", nil, errors.New("quota: SQLStore has no Filter")
	}
	cond, args := s.Filter(c)
	if strings.TrimSpace(cond) == "" {
		return "", nil, fmt.Errorf("quota: SQLStore: empty filter for %s", c)
	}
	timeArg := s.TimeArg
	if timeArg == nil {
		timeArg = func(t time.Time) any { return t.UTC() }
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE (%s) AND %s >= ? AND %s < ?",
		sel, s.Table, cond, s.TimeColumn, s.TimeColumn)
	all := append(append([]any(nil), args...), timeArg(from), timeArg(to))
	if strings.Count(q, "?") != len(all) {
		return "", nil, fmt.Errorf("quota: SQLStore: filter for %s has %d arguments for %d placeholders",
			c, len(args), strings.Count(cond, "?"))
	}
	if s.Placeholder != nil {
		var b strings.Builder
		n := 0
		for _, r := range q {
			if r == '?' {
				n++
				b.WriteString(s.Placeholder(n))
				continue
			}
			b.WriteRune(r)
		}
		q = b.String()
	}
	return q, all, nil
}

// Sum implements Store.
func (s *SQLStore) Sum(ctx context.Context, c Counter, from, to time.Time) (int64, error) {
	q, args, err := s.query(c, "COALESCE(SUM("+s.AmountColumn+"), 0)", from, to)
	if err != nil {
		return 0, err
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// Entries implements Store.
func (s *SQLStore) Entries(ctx context.Context, c Counter, from, to time.Time) ([]Entry, error) {
	q, args, err := s.query(c, s.TimeColumn+", "+s.AmountColumn, from, to)
	if err != nil {
		return nil, err
	}
	q += " ORDER BY " + s.TimeColumn
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scan := s.ScanTime
	if scan == nil {
		scan = scanTime
	}
	var out []Entry
	for rows.Next() {
		var (
			raw    any
			amount int64
		)
		if err := rows.Scan(&raw, &amount); err != nil {
			return nil, err
		}
		at, err := scan(raw)
		if err != nil {
			return nil, fmt.Errorf("quota: SQLStore: %s: %w", s.TimeColumn, err)
		}
		out = append(out, Entry{At: at, Amount: amount})
	}
	return out, rows.Err()
}

func scanTime(src any) (time.Time, error) {
	switch v := src.(type) {
	case time.Time:
		return v, nil
	case string:
		return time.Parse(time.RFC3339Nano, v)
	case []byte:
		return time.Parse(time.RFC3339Nano, string(v))
	default:
		return time.Time{}, fmt.Errorf("cannot read %T as a time; set ScanTime", src)
	}
}
