package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Sensitivity is the highest classification found or required by the policy.
type Sensitivity string

const (
	Public  Sensitivity = "public"
	Private Sensitivity = "private"
	Secret  Sensitivity = "secret"
)

// ErrorClass is a fixed failure label, never an input-derived error message.
type ErrorClass string

const (
	ErrorOversized   ErrorClass = "oversized"
	ErrorInvalidJSON ErrorClass = "invalid_json"
	ErrorOpaque      ErrorClass = "opaque"
	ErrorDepth       ErrorClass = "depth"
)

// Mode controls field retention. The zero value requires an explicit allowlist.
type Mode uint8

const (
	Allowlist Mode = iota
	Denylist
)

// Format declares how the payload can be inspected. JSON is the default.
type Format uint8

const (
	JSON Format = iota
	Opaque
)

const DefaultMaxBytes = 64 << 10
const maxPayloadDepth = 64

// Policy describes one event kind. Construct once with NewSanitizer.
type Policy struct {
	Mode Mode
	// AllowedPaths are RFC 6901 pointers to retained values. A '*' segment
	// matches any array index. Container paths do not authorize their children:
	// list each child or array-element path. Empty allowlist removes all values.
	AllowedPaths []string
	Sensitivity  Sensitivity
	Format       Format
	// MaxBytes caps input bytes, before parsing. Zero means DefaultMaxBytes.
	MaxBytes int
}

// Report contains classifications and counts, never payload values. Paths are
// RFC 6901 pointers ("" is the root); sensitive object keys are masked in paths
// and their members removed. Redactions counts replaced spans and removed
// values; dropping a subtree counts once. Paths are unique, in input order.
type Report struct {
	Redactions  uint32      `json:"redactions"`
	Paths       []string    `json:"paths,omitempty"`
	Sensitivity Sensitivity `json:"sensitivity"`
	PII         bool        `json:"pii"`
	ErrorClass  ErrorClass  `json:"error_class,omitempty"`
}

type pathNode struct {
	terminal bool
	children map[string]*pathNode
}

// Sanitizer is an immutable, concurrently usable payload policy over Redactor's
// range engine. PII rules are mandatory here; Default remains credential-only.
type Sanitizer struct {
	r           *Redactor
	mode        Mode
	format      Format
	maxBytes    int
	sensitivity Sensitivity
	paths       *pathNode
}

// NewSanitizer validates and copies a per-kind policy. Options customize the
// credential Redactor; they cannot disable the mandatory PII rules.
func NewSanitizer(p Policy, opts ...Option) (*Sanitizer, error) {
	if p.Mode != Allowlist && p.Mode != Denylist {
		return nil, errors.New("redact: invalid sanitize mode")
	}
	if p.Format != JSON && p.Format != Opaque {
		return nil, errors.New("redact: invalid payload format")
	}
	if p.MaxBytes < 0 {
		return nil, errors.New("redact: invalid payload size limit")
	}
	if p.MaxBytes == 0 {
		p.MaxBytes = DefaultMaxBytes
	}
	if p.Sensitivity == "" {
		p.Sensitivity = Public
	}
	if p.Sensitivity != Public && p.Sensitivity != Private && p.Sensitivity != Secret {
		return nil, errors.New("redact: invalid sensitivity")
	}
	root := &pathNode{}
	for _, path := range p.AllowedPaths {
		parts, err := pointerParts(path)
		if err != nil {
			return nil, err
		}
		n := root
		for _, part := range parts {
			if n.children == nil {
				n.children = map[string]*pathNode{}
			}
			if n.children[part] == nil {
				n.children[part] = &pathNode{}
			}
			n = n.children[part]
		}
		n.terminal = true
	}
	r, err := New(opts...)
	if err != nil {
		return nil, err
	}
	r.rules = append(r.rules, piiRules()...)
	return &Sanitizer{r: r, mode: p.Mode, format: p.Format, maxBytes: p.MaxBytes, sensitivity: p.Sensitivity, paths: root}, nil
}

func pointerParts(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if !utf8.ValidString(p) || p[0] != '/' {
		return nil, errors.New("redact: invalid allowed JSON pointer")
	}
	parts := strings.Split(p[1:], "/")
	for i, s := range parts {
		for j := 0; j < len(s); j++ {
			if s[j] == '~' {
				if j+1 == len(s) || s[j+1] != '0' && s[j+1] != '1' {
					return nil, errors.New("redact: invalid allowed JSON pointer escape")
				}
				j++
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func pointerChild(parent, key string) string {
	return parent + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// Sanitize returns independent, valid JSON bytes. Failure always returns a safe
// tombstone and ErrorClass; callers must then mark the envelope unsanitized.
// Duplicate keys, invalid UTF-8, and excessive nesting also fail closed.
func (s *Sanitizer) Sanitize(payload []byte) ([]byte, Report) {
	rep := Report{Sensitivity: s.sensitivity}
	fail := func(class ErrorClass) ([]byte, Report) {
		rep.Paths = nil
		rep.Redactions = 0
		rep.Sensitivity = Secret // uninspected data cannot be classified as public
		rep.ErrorClass = class
		return []byte(`{"sanitize_error":"` + string(class) + `"}`), rep
	}
	if len(payload) > s.maxBytes {
		return fail(ErrorOversized)
	}
	if s.format == Opaque {
		return fail(ErrorOpaque)
	}
	if !utf8.Valid(payload) {
		return fail(ErrorInvalidJSON)
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	value, err := readPayloadValue(dec, 0)
	if errors.Is(err, errPayloadDepth) {
		return fail(ErrorDepth)
	}
	if err != nil {
		return fail(ErrorInvalidJSON)
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return fail(ErrorInvalidJSON)
	}
	st := payloadWalk{s: s, rep: &rep, seen: map[string]bool{}}
	out, keep := st.walk(value, s.paths, "", s.mode == Denylist)
	if !keep {
		out = nil
	}
	var b bytes.Buffer
	writeJSON(&b, out)
	if b.Len() > s.maxBytes {
		return fail(ErrorOversized)
	}
	return b.Bytes(), rep
}

var errPayloadDepth = errors.New("redact: payload nesting limit")

func readPayloadValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxPayloadDepth {
		return nil, errPayloadDepth
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := object{}
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok || seen[key] {
					return nil, errors.New("redact: duplicate or invalid object key")
				}
				seen[key] = true
				v, err := readPayloadValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				obj = append(obj, pair{key, v})
			}
			_, err = dec.Token()
			return obj, err
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := readPayloadValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err = dec.Token()
			return arr, err
		}
	}
	return tok, nil
}

type payloadWalk struct {
	s    *Sanitizer
	rep  *Report
	seen map[string]bool
}

func (w *payloadWalk) classify(name string) {
	if isPIIRule(name) {
		w.rep.PII = true
		w.raise(Private)
	} else {
		w.raise(Secret)
	}
}
func (w *payloadWalk) raise(c Sensitivity) {
	if c == Secret || c == Private && w.rep.Sensitivity == Public {
		w.rep.Sensitivity = c
	}
}
func (w *payloadWalk) record(path string, n int) {
	if n == 0 {
		return
	}
	w.rep.Redactions += uint32(n)
	if !w.seen[path] {
		w.seen[path] = true
		w.rep.Paths = append(w.rep.Paths, path)
	}
}
func (w *payloadWalk) text(t string) (string, int) {
	return w.s.r.replaceFindings(t, w.s.r.scan(t, w.classify))
}
func (w *payloadWalk) sensitive(key string, v any) bool {
	if !w.s.r.policy.sensitive(key) {
		return false
	}
	switch t := v.(type) {
	case bool, nil:
		return false
	case string:
		return !w.s.r.isPlaceholder(t)
	default:
		return true
	}
}

// inspect preserves PII and sensitivity even when an ancestor is removed whole.
func (w *payloadWalk) inspect(v any) {
	switch x := v.(type) {
	case object:
		for _, p := range x {
			w.text(p.k)
			if w.sensitive(p.k, p.v) {
				w.raise(Secret)
			}
			w.inspect(p.v)
		}
	case []any:
		for _, e := range x {
			w.inspect(e)
		}
	case string:
		w.text(x)
	case json.Number:
		w.text(x.String())
	}
}
func child(n *pathNode, key string) *pathNode {
	if n == nil {
		return nil
	}
	return n.children[key]
}

func (w *payloadWalk) walk(v any, n *pathNode, path string, all bool) (any, bool) {
	if !all && n == nil {
		if v == nil {
			return nil, false
		}
		w.inspect(v)
		w.record(path, 1)
		w.raise(Secret)
		return nil, false
	}
	switch x := v.(type) {
	case object:
		out := object{}
		for _, p := range x {
			_, nkey := w.text(p.k)
			key := p.k
			if nkey > 0 {
				key = "*"
			}
			fieldPath := pointerChild(path, key)
			if nkey > 0 {
				w.inspect(p.v)
				w.record(fieldPath, 1)
				continue
			}
			if w.sensitive(p.k, p.v) {
				w.inspect(p.v)
				w.raise(Secret)
				// A denied field is dropped, rather than adding a new allowlisted key.
				if !all && child(n, p.k) == nil {
					w.record(fieldPath, 1)
					continue
				}
				w.record(fieldPath, 1)
				out = append(out, pair{p.k, w.s.r.marker})
				continue
			}
			val, keep := w.walk(p.v, child(n, p.k), fieldPath, all)
			if keep {
				out = append(out, pair{p.k, val})
			}
		}
		return out, true
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			index := strconv.Itoa(i)
			cn := child(n, index)
			if wildcard := child(n, "*"); wildcard != nil {
				cn = unionPath(cn, wildcard)
			}
			// Removed elements become null, retaining positions used by JSON pointers.
			out[i], _ = w.walk(e, cn, pointerChild(path, index), all)
		}
		return out, true
	default:
		if !all && !n.terminal {
			if v == nil {
				return nil, false
			}
			w.inspect(v)
			w.record(path, 1)
			w.raise(Secret)
			return nil, false
		}
		switch t := v.(type) {
		case string:
			out, count := w.text(t)
			w.record(path, count)
			return out, true
		case json.Number:
			out, count := w.text(t.String())
			w.record(path, count)
			if count > 0 {
				return out, true
			}
		}
		return v, true
	}
}

func unionPath(a, b *pathNode) *pathNode {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	n := &pathNode{terminal: a.terminal || b.terminal, children: map[string]*pathNode{}}
	for k, v := range a.children {
		n.children[k] = v
	}
	for k, v := range b.children {
		n.children[k] = unionPath(n.children[k], v)
	}
	return n
}
