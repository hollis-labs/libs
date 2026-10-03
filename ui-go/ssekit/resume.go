package ssekit

import (
	"net/http"
	"strings"
)

// Precedence decides which cursor wins when a request carries both a
// Last-Event-ID header and a query parameter.
type Precedence struct {
	kind  int
	newer func(a, b string) int
}

var (
	// HeaderFirst prefers the Last-Event-ID header. It is the default: a
	// browser sends it only when reconnecting, so it is at least as new as the
	// initial ?from= that stays fixed in the URL.
	HeaderFirst = Precedence{kind: 0}
	// QueryFirst prefers the query parameter.
	QueryFirst = Precedence{kind: 1}
)

// Newest prefers whichever cursor cmp says is newer. cmp(a, b) returns a
// positive number when a is newer than b, negative when b is newer and zero
// when equal; the header wins ties. The library never parses a cursor, so the
// arithmetic is yours.
func Newest(cmp func(a, b string) int) Precedence {
	return Precedence{kind: 2, newer: cmp}
}

// ResumeOption configures ResumeCursor.
type ResumeOption func(*resumeConfig)

type resumeConfig struct {
	keys []string
	prec Precedence
}

// WithQueryKeys names the query parameters that may carry a cursor, in order of
// preference; the first non-empty one is used. By default only the header is
// read.
func WithQueryKeys(keys ...string) ResumeOption {
	return func(c *resumeConfig) { c.keys = keys }
}

// WithPrecedence sets which source wins when both are present (default
// HeaderFirst).
func WithPrecedence(p Precedence) ResumeOption {
	return func(c *resumeConfig) { c.prec = p }
}

// ResumeCursor extracts the client's resume cursor from r as an opaque string.
// ok is false when the request carries none. Values are trimmed; an empty value
// counts as absent. It does not validate or compare cursors.
func ResumeCursor(r *http.Request, o ...ResumeOption) (cursor string, ok bool) {
	cfg := resumeConfig{prec: HeaderFirst}
	for _, f := range o {
		f(&cfg)
	}
	header := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	var query string
	if len(cfg.keys) > 0 {
		q := r.URL.Query()
		for _, k := range cfg.keys {
			if v := strings.TrimSpace(q.Get(k)); v != "" {
				query = v
				break
			}
		}
	}
	switch {
	case header == "" && query == "":
		return "", false
	case header == "":
		return query, true
	case query == "":
		return header, true
	}
	switch cfg.prec.kind {
	case 1:
		return query, true
	case 2:
		if cfg.prec.newer != nil && cfg.prec.newer(header, query) < 0 {
			return query, true
		}
		return header, true
	default:
		return header, true
	}
}
