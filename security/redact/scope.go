package redact

import (
	"encoding/base64"
	"encoding/json"
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FloorLen is the shortest value a Scope will ever look for. Below it a value is a
// flag or a word ("1", "on", "pass") and replacing it would cut it out of ordinary text.
const FloorLen = 4

// DefaultMinLen is the default shortest value a Scope protects. Values are matched
// anywhere in the text, so a short common value would be cut out of every sentence
// that contains it ("test" out of "tested"); a credential worth protecting this
// way is longer.
const DefaultMinLen = 8

// Scope is a concurrency-safe set of known secret values: the credentials an
// application has just resolved. Exact matching is precise where patterns can only
// guess, so a Scope is the strongest protection for a secret you hold; the pattern
// rules protect the ones you do not.
//
// Each value is also matched in the forms it takes in rendered text: URL-escaped,
// JSON-escaped (with and without HTML escaping), HTML-escaped, Go-quoted and base64.
type Scope struct {
	mu          sync.RWMutex
	min         int
	forms       map[string]bool
	sorted      []string // longest first
	unprotected []string // names of values too short to protect
}

// ScopeOption configures [NewScope].
type ScopeOption func(*Scope)

// WithMinLen sets the shortest value protected (never below [FloorLen]).
func WithMinLen(n int) ScopeOption { return func(s *Scope) { s.min = max(n, FloorLen) } }

// NewScope returns an empty Scope.
func NewScope(opts ...ScopeOption) *Scope {
	s := &Scope{min: DefaultMinLen, forms: map[string]bool{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Add records a secret value. name identifies it in [Scope.Unprotected] (a variable
// or secret name; never the value). A value shorter than the minimum is not
// protected: Add reports false and remembers the name, so a caller can say that a
// secret is not covered instead of believing it is.
func (s *Scope) Add(name, value string) bool {
	if s == nil || value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(value) < s.min {
		s.unprotected = append(s.unprotected, name)
		return false
	}
	for _, f := range Forms(value) {
		if len(f) >= FloorLen {
			s.forms[f] = true
		}
	}
	// A new slice every time: find() holds the old one without a lock, and rewriting
	// it in place would race with that reader.
	next := make([]string, 0, len(s.forms))
	for f := range s.forms {
		next = append(next, f)
	}
	sort.Slice(next, func(i, j int) bool {
		if len(next[i]) != len(next[j]) {
			return len(next[i]) > len(next[j])
		}
		return next[i] < next[j]
	})
	s.sorted = next
	return true
}

// Unprotected returns the names of values that were too short to protect.
func (s *Scope) Unprotected() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.unprotected...)
}

// Forms returns the strings value takes in rendered text.
func Forms(value string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	add(value)
	add(url.QueryEscape(value))
	add(url.PathEscape(value))
	add(html.EscapeString(value))
	if b, err := json.Marshal(value); err == nil && len(b) >= 2 {
		add(string(b[1 : len(b)-1])) // json.Marshal escapes <, > and &
	}
	add(jsonNoHTML(value))
	if q := strconv.Quote(value); len(q) >= 2 {
		add(q[1 : len(q)-1])
	}
	raw := []byte(value)
	add(base64.StdEncoding.EncodeToString(raw))
	add(base64.RawStdEncoding.EncodeToString(raw))
	add(base64.URLEncoding.EncodeToString(raw))
	add(base64.RawURLEncoding.EncodeToString(raw))
	return out
}

func jsonNoHTML(v string) string {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	out := strings.TrimSuffix(sb.String(), "\n")
	if len(out) < 2 {
		return ""
	}
	return out[1 : len(out)-1]
}

// find reports every occurrence of every known form in t. Findings are merged by the
// caller, so a secret that contains another is covered whole whatever the order.
func (s *Scope) find(t string, add func(a, b int)) {
	if s == nil {
		return
	}
	s.mu.RLock()
	forms := s.sorted
	s.mu.RUnlock()
	for _, f := range forms {
		for from := 0; ; {
			i := strings.Index(t[from:], f)
			if i < 0 {
				break
			}
			add(from+i, from+i+len(f))
			from += i + len(f)
		}
	}
}

// edgeMin is the shortest fragment treated as part of a cut-off secret.
const edgeMin = 6

// edges returns the range of s left after blanking a fragment of a known value at
// the start (its tail end) and at the end (its leading part).
func (s *Scope) edges(t string) (start, end int) {
	start, end = 0, len(t)
	s.mu.RLock()
	forms := s.sorted
	s.mu.RUnlock()
	for _, f := range forms {
		if len(f) < edgeMin+1 {
			continue
		}
		for k := min(len(f)-1, len(t)); k >= edgeMin; k-- {
			if strings.HasSuffix(t, f[:k]) {
				end = min(end, len(t)-k)
				break
			}
		}
		for k := min(len(f)-1, len(t)); k >= edgeMin; k-- {
			if strings.HasPrefix(t, f[len(f)-k:]) {
				start = max(start, k)
				break
			}
		}
	}
	if start > end {
		start = end
	}
	return start, end
}
