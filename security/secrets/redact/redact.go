// Package redact scrubs known secret values out of text.
//
// It is the complete-string redactor: it replaces exact occurrences of the
// values it knows and leaves an ordinary word that merely starts or ends like a
// secret intact. Error text is whole, so this is the right shape for it. A
// stderr tail that can be cut mid-value needs edge-fragment blanking, which is
// not in the MVP (see the README).
//
// A [Set] also registers the URL-escaped and JSON-escaped forms of each value,
// because a credential that travels inside a URL or a JSON string no longer
// matches its raw form.
package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Marker replaces each redacted value.
const Marker = "[redacted]"

// MinLen is the shortest value treated as a secret. Shorter values (an env
// flag such as "1" or "on") are never credentials, and replacing them would
// mangle every number or word that contains them. A shorter value passed to
// [Set.Add] is counted by [Set.Unprotected] so a caller can report that it is
// not covered, rather than silently believing it is.
const MinLen = 4

// Text returns text with every occurrence of each secret replaced by Marker.
// Longer secrets are replaced first, so one secret that contains another is
// never left half-scrubbed. Values shorter than MinLen are ignored.
func Text(text string, secrets []string) string {
	if text == "" || len(secrets) == 0 {
		return text
	}
	ordered := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if len(s) >= MinLen {
			ordered = append(ordered, s)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, s := range ordered {
		text = strings.ReplaceAll(text, s, Marker)
	}
	return text
}

// Forms returns the raw value and its URL-escaped and JSON-escaped forms,
// without duplicates.
func Forms(value string) []string {
	forms := []string{value}
	add := func(f string) {
		for _, e := range forms {
			if e == f {
				return
			}
		}
		forms = append(forms, f)
	}
	add(url.QueryEscape(value))
	add(url.PathEscape(value))
	// encoding/json escapes <, > and & by default; other encoders do not. A
	// credential can appear in either form.
	if b, err := json.Marshal(value); err == nil && len(b) >= 2 {
		add(string(b[1 : len(b)-1]))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err == nil {
		if b := bytes.TrimSuffix(buf.Bytes(), []byte("\n")); len(b) >= 2 {
			add(string(b[1 : len(b)-1]))
		}
	}
	return forms
}

// Set is a concurrency-safe set of secret values. The zero value is ready
// to use; a nil *Set redacts nothing and ignores Add.
type Set struct {
	mu          sync.RWMutex
	values      map[string]struct{}
	unprotected int
}

// Add records values as secrets, with their escaped forms. Values shorter than
// MinLen are not recorded; they are counted in Unprotected.
func (s *Set) Add(values ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range values {
		if v == "" {
			continue
		}
		if len(v) < MinLen {
			s.unprotected++
			continue
		}
		if s.values == nil {
			s.values = map[string]struct{}{}
		}
		for _, f := range Forms(v) {
			s.values[f] = struct{}{}
		}
	}
}

// Unprotected reports how many added values were too short to redact.
func (s *Set) Unprotected() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unprotected
}

// Redact returns text with every value in the set replaced by Marker.
func (s *Set) Redact(text string) string {
	if s == nil || text == "" {
		return text
	}
	s.mu.RLock()
	secrets := make([]string, 0, len(s.values))
	for v := range s.values {
		secrets = append(secrets, v)
	}
	s.mu.RUnlock()
	return Text(text, secrets)
}

// Error wraps err so its text is redacted while errors.Is and errors.As still
// reach the original cause. A nil err, or one with nothing to scrub, is
// returned as it was.
func (s *Set) Error(err error) error {
	if err == nil || s == nil {
		return err
	}
	text := err.Error()
	got := s.Redact(text)
	if got == text {
		return err
	}
	return &redactedError{text: got, err: err}
}

type redactedError struct {
	text string
	err  error
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.err }

// Remember wraps a secret resolver so that every value it returns is added
// to the set before the caller can use it. A credential resolved for a call
// is then known by the time that call's error is recorded.
func (s *Set) Remember(resolve func(context.Context) (string, error)) func(context.Context) (string, error) {
	if resolve == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		v, err := resolve(ctx)
		if err == nil {
			s.Add(v)
		}
		return v, err
	}
}
