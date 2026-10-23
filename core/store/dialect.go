package store

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Dialect is the per-engine adapter. Everything that must differ between
// SQLite and Postgres is behind it, so callers and the subpackages never branch
// on the driver name.
type Dialect interface {
	// Name is "sqlite" or "postgres".
	Name() Driver
	// Rebind rewrites "?" placeholders for the engine.
	Rebind(query string) string
	// EncodeTime turns a Go time into the bound parameter for the engine:
	// fixed-width UTC text on SQLite, a microsecond-truncated UTC time on
	// Postgres.
	EncodeTime(t time.Time) any
	// JSONText renders an expression extracting the value at path from a JSON
	// column as text. Path segments are object keys or array indexes.
	JSONText(column string, path ...string) (string, error)
	// JSONInt, JSONFloat and JSONBool render the same extraction cast to a
	// number or boolean, so comparisons and ordering behave identically.
	JSONInt(column string, path ...string) (string, error)
	JSONFloat(column string, path ...string) (string, error)
	JSONBool(column string, path ...string) (string, error)
	// ClaimLock is the row-locking suffix for a claim query: "FOR UPDATE SKIP
	// LOCKED" on Postgres, empty on SQLite where IMMEDIATE transactions already
	// serialise writers.
	ClaimLock() string
	// SupportsReturning is true for both supported engines; it exists so a
	// future dialect can opt out loudly.
	SupportsReturning() bool
}

// DialectFor returns the adapter for a driver.
func DialectFor(d Driver) (Dialect, error) {
	switch d {
	case DriverSQLite:
		return sqliteDialect{}, nil
	case DriverPostgres:
		return postgresDialect{}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownDriver, d)
}

// TimeFormat is the SQLite text form of a timestamp: UTC, fixed width, so
// string order is time order. RFC3339Nano trims trailing zeros and would not
// sort.
const TimeFormat = "2006-01-02T15:04:05.000000000Z"

// Canonical returns t as stored by both engines: UTC, truncated to
// microseconds (the Postgres timestamptz precision), so a value round-trips
// identically everywhere.
func Canonical(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

var (
	identRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
	segmentRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// ErrBadIdentifier is returned when a column or path is spliced into SQL and is
// not a plain identifier.
var ErrBadIdentifier = errors.New("store: not a plain identifier")

// CheckIdentifier validates a column or table name (optionally qualified once).
func CheckIdentifier(s string) error {
	if !identRe.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrBadIdentifier, s)
	}
	return nil
}

func checkJSON(column string, path []string) error {
	if err := CheckIdentifier(column); err != nil {
		return err
	}
	if len(path) == 0 {
		return fmt.Errorf("%w: empty json path", ErrBadIdentifier)
	}
	for _, p := range path {
		if !segmentRe.MatchString(p) {
			return fmt.Errorf("%w: json path segment %q", ErrBadIdentifier, p)
		}
	}
	return nil
}

// LikeEscape escapes %, _ and the escape character itself for a LIKE pattern
// used with ESCAPE '\'.
func LikeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// LikeInsensitive renders a case-insensitive LIKE that behaves the same on both
// engines: lower() on both sides, backslash as the escape character.
func LikeInsensitive(column string) (string, error) {
	if err := CheckIdentifier(column); err != nil {
		return "", err
	}
	return "lower(" + column + ") LIKE lower(?) ESCAPE '\\'", nil
}

// ---- SQLite ----

type sqliteDialect struct{}

func (sqliteDialect) Name() Driver               { return DriverSQLite }
func (sqliteDialect) Rebind(q string) string     { return strings.ReplaceAll(q, "??", "?") }
func (sqliteDialect) EncodeTime(t time.Time) any { return Canonical(t).Format(TimeFormat) }
func (sqliteDialect) ClaimLock() string          { return "" }
func (sqliteDialect) SupportsReturning() bool    { return true }
func sqlitePath(path []string) string {
	var b strings.Builder
	b.WriteString("$")
	for _, p := range path {
		if _, err := strconv.Atoi(p); err == nil {
			b.WriteString("[" + p + "]")
		} else {
			b.WriteString("." + p)
		}
	}
	return b.String()
}
func (sqliteDialect) JSONText(c string, p ...string) (string, error) {
	if err := checkJSON(c, p); err != nil {
		return "", err
	}
	return "CAST(json_extract(" + c + ", '" + sqlitePath(p) + "') AS TEXT)", nil
}
func (sqliteDialect) JSONInt(c string, p ...string) (string, error) {
	if err := checkJSON(c, p); err != nil {
		return "", err
	}
	return "CAST(json_extract(" + c + ", '" + sqlitePath(p) + "') AS INTEGER)", nil
}
func (sqliteDialect) JSONFloat(c string, p ...string) (string, error) {
	if err := checkJSON(c, p); err != nil {
		return "", err
	}
	return "CAST(json_extract(" + c + ", '" + sqlitePath(p) + "') AS REAL)", nil
}
func (sqliteDialect) JSONBool(c string, p ...string) (string, error) {
	if err := checkJSON(c, p); err != nil {
		return "", err
	}
	return "(json_extract(" + c + ", '" + sqlitePath(p) + "') = 1)", nil
}

// ---- Postgres ----

type postgresDialect struct{}

func (postgresDialect) Name() Driver               { return DriverPostgres }
func (postgresDialect) Rebind(q string) string     { return Rebind(q) }
func (postgresDialect) EncodeTime(t time.Time) any { return Canonical(t) }
func (postgresDialect) ClaimLock() string          { return "FOR UPDATE SKIP LOCKED" }
func (postgresDialect) SupportsReturning() bool    { return true }
func pgPath(c string, path []string) string {
	return "((" + c + ")::jsonb #>> '{" + strings.Join(path, ",") + "}')"
}
func (postgresDialect) JSONText(c string, p ...string) (string, error) {
	if err := checkJSON(c, p); err != nil {
		return "", err
	}
	return pgPath(c, p), nil
}
func (postgresDialect) JSONInt(c string, p ...string) (string, error) {
	if err := checkJSON(c, p); err != nil {
		return "", err
	}
	return pgPath(c, p) + "::bigint", nil
}
func (postgresDialect) JSONFloat(c string, p ...string) (string, error) {
	if err := checkJSON(c, p); err != nil {
		return "", err
	}
	return pgPath(c, p) + "::double precision", nil
}
func (postgresDialect) JSONBool(c string, p ...string) (string, error) {
	if err := checkJSON(c, p); err != nil {
		return "", err
	}
	return pgPath(c, p) + "::boolean", nil
}
