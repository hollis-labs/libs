package store

import (
	"database/sql"
	"fmt"
	"time"
)

// ScanTime returns a scanner that fills dst from a timestamp column on either
// engine: fixed-width text on SQLite, a native time on Postgres. Use it in
// Scan: row.Scan(store.ScanTime(&t)).
func ScanTime(dst *time.Time) sql.Scanner { return timeScanner{dst: dst} }

// ScanNullTime is ScanTime for a nullable column: NULL leaves *dst nil.
func ScanNullTime(dst **time.Time) sql.Scanner { return timeScanner{pdst: dst} }

type timeScanner struct {
	dst  *time.Time
	pdst **time.Time
}

func (s timeScanner) Scan(v any) error {
	if v == nil {
		if s.pdst != nil {
			*s.pdst = nil
			return nil
		}
		return fmt.Errorf("store: NULL scanned into a non-null time")
	}
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case string:
		p, err := time.Parse(TimeFormat, x)
		if err != nil {
			return fmt.Errorf("store: parse time %q: %w", x, err)
		}
		t = p
	case []byte:
		return s.Scan(string(x))
	default:
		return fmt.Errorf("store: cannot scan %T into a time", v)
	}
	t = t.UTC()
	if s.pdst != nil {
		*s.pdst = &t
		return nil
	}
	*s.dst = t
	return nil
}
