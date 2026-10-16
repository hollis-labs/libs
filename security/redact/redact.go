package redact

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Marker replaces each redacted value unless [WithMarker] says otherwise. One
// marker everywhere is deliberate: output that is redacted twice, or by two
// components, must still read the same.
const Marker = "[REDACTED]"

// Redactor removes credential material from text. It is immutable after [New]
// (except for the values a [Scope] it uses learns later) and safe for concurrent use.
type Redactor struct {
	marker string
	rules  []rule
	policy *keyPolicy
	refs   []string // lower-case reference prefixes: "op://"
	scope  *Scope
}

// Finding is one redacted range of the original text. It carries no secret.
type Finding struct {
	Rule       string // the rule that found it; [RuleScope] for a known value
	Start, End int    // byte offsets in the original text
}

// RuleScope names findings that came from a known value in a [Scope].
const RuleScope = "known-value"

// Rule is an application-supplied pattern. Group is the capture group to redact
// (0 for the whole match).
type Rule struct {
	Name    string
	Pattern string
	Group   int
}

// Option configures [New].
type Option func(*config) error

type config struct {
	marker     string
	scope      *Scope
	extra      []rule
	disabled   map[string]bool
	refs       []string
	benign     []string
	sensitive  []string
	heuristics bool
}

// WithMarker replaces the default [Marker].
func WithMarker(m string) Option {
	return func(c *config) error {
		if m == "" {
			return errors.New("redact: marker must not be empty")
		}
		c.marker = m
		return nil
	}
}

// WithScope adds known secret values. The Scope may be filled after New; the
// Redactor reads it on every call.
func WithScope(s *Scope) Option { return func(c *config) error { c.scope = s; return nil } }

// WithRule adds an application pattern. An invalid pattern or group makes [New]
// fail: a redactor that silently drops a rule it was asked to apply is worse than
// one that refuses to start.
func WithRule(rl Rule) Option {
	return func(c *config) error {
		re, err := regexp.Compile(rl.Pattern)
		if err != nil {
			return fmt.Errorf("redact: rule %q: %w", rl.Name, err)
		}
		if rl.Group < 0 || rl.Group > re.NumSubexp() {
			return fmt.Errorf("redact: rule %q: group %d does not exist in the pattern", rl.Name, rl.Group)
		}
		name := rl.Name
		if name == "" {
			name = "custom"
		}
		c.extra = append(c.extra, rule{name, regexRule(re, rl.Group)})
		return nil
	}
}

// WithoutRules disables built-in rules by name (the Rule* constants).
func WithoutRules(names ...string) Option {
	return func(c *config) error {
		for _, n := range names {
			c.disabled[n] = true
		}
		return nil
	}
}

// WithReferenceSchemes adds URI schemes whose values name a secret held
// elsewhere and are therefore not redacted ("op", "keychain", ...). Built in:
// op, keychain, keyring, helper, keeper, vault, file, env, secretref.
func WithReferenceSchemes(schemes ...string) Option {
	return func(c *config) error {
		for _, s := range schemes {
			c.refs = append(c.refs, strings.ToLower(strings.TrimSuffix(s, "://"))+"://")
		}
		return nil
	}
}

// WithBenignKeys marks field names that look sensitive but are not (a plugin
// called "onepassword", a field "token_bucket"). Names are matched the way keys
// are everywhere: case and separators do not matter.
func WithBenignKeys(names ...string) Option {
	return func(c *config) error { c.benign = append(c.benign, names...); return nil }
}

// WithSensitiveKeys marks application-specific field names as secret.
func WithSensitiveKeys(names ...string) Option {
	return func(c *config) error { c.sensitive = append(c.sensitive, names...); return nil }
}

// WithHeuristics enables rules that can match ordinary text: for now, an unlabeled
// 40-character mixed-case base64 string (the shape of an AWS secret access key).
func WithHeuristics() Option { return func(c *config) error { c.heuristics = true; return nil } }

var defaultRefs = []string{"op://", "keychain://", "keyring://", "helper://", "keeper://", "vault://", "file://", "env://", "secretref://"}

// New builds a Redactor.
func New(opts ...Option) (*Redactor, error) {
	c := &config{marker: Marker, disabled: map[string]bool{}}
	for _, o := range opts {
		if err := o(c); err != nil {
			return nil, err
		}
	}
	r := &Redactor{marker: c.marker, scope: c.scope, refs: append(append([]string{}, defaultRefs...), c.refs...)}
	r.policy = &keyPolicy{extraBenign: map[string]bool{}, extraSensitive: map[string]bool{}}
	for _, n := range c.benign {
		r.policy.extraBenign[normKey(n)] = true
	}
	for _, n := range c.sensitive {
		r.policy.extraSensitive[normKey(n)] = true
	}
	for _, rl := range defaultRules() {
		if !c.disabled[rl.name] {
			r.rules = append(r.rules, rl)
		}
	}
	if c.heuristics && !c.disabled[RuleAWSSecretKey] {
		r.rules = append(r.rules, rule{RuleAWSSecretKey, findAWSSecret})
	}
	r.rules = append(r.rules, c.extra...)
	return r, nil
}

var (
	defaultOnce sync.Once
	defaultR    *Redactor
)

// Default returns a shared Redactor with the built-in rules and no known values.
func Default() *Redactor {
	defaultOnce.Do(func() { defaultR, _ = New() })
	return defaultR
}

// Text redacts the built-in rule set over s.
func Text(s string) string { return Default().Text(s) }

// Text returns s with credential material replaced by the marker. Surrounding
// text, newlines and non-secret fields are left as they were; redacting the
// result again changes nothing.
func (r *Redactor) Text(s string) string {
	out, _ := r.TextCount(s)
	return out
}

// Bytes is [Redactor.Text] for byte slices.
func (r *Redactor) Bytes(b []byte) []byte { return []byte(r.Text(string(b))) }

// TextCount is [Redactor.Text] that also reports how many values it replaced.
func (r *Redactor) TextCount(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	return r.replaceFindings(s, r.Scan(s))
}

func (r *Redactor) replaceFindings(s string, fs []Finding) (string, int) {
	if len(fs) == 0 {
		return s, 0
	}
	var b strings.Builder
	b.Grow(len(s))
	pos, n := 0, 0
	for _, f := range fs {
		if s[f.Start:f.End] == r.marker {
			continue // already redacted: keeps repeated redaction a no-op
		}
		b.WriteString(s[pos:f.Start])
		b.WriteString(r.marker)
		pos = f.End
		n++
	}
	b.WriteString(s[pos:])
	return b.String(), n
}

// Scan reports what would be redacted in s, without the values. The findings are
// merged: overlapping or touching ranges are one.
func (r *Redactor) Scan(s string) []Finding {
	return r.scan(s, nil)
}

// observe runs before merging so overlapping rules retain their classification.
func (r *Redactor) scan(s string, observe func(string)) []Finding {
	return r.scanExcept(s, observe, "")
}

func (r *Redactor) scanExcept(s string, observe func(string), skip string) []Finding {
	if s == "" {
		return nil
	}
	v := newView(s)
	st := newScanText(v.text)
	var raw []Finding
	for _, rl := range r.rules {
		if rl.name == skip {
			continue
		}
		name := rl.name
		rl.find(r, st, func(a, b int) {
			if b > a {
				raw = append(raw, Finding{name, v.at(a), v.at(b)})
				if observe != nil {
					observe(name)
				}
			}
		})
	}
	if r.scope != nil {
		r.scope.find(v.text, func(a, b int) {
			if b > a {
				raw = append(raw, Finding{RuleScope, v.at(a), v.at(b)})
				if observe != nil {
					observe(RuleScope)
				}
			}
		})
	}
	return merge(raw)
}

func merge(fs []Finding) []Finding {
	if len(fs) < 2 {
		return fs
	}
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Start != fs[j].Start {
			return fs[i].Start < fs[j].Start
		}
		return fs[i].End > fs[j].End
	})
	out := fs[:1]
	for _, f := range fs[1:] {
		last := &out[len(out)-1]
		if f.Start <= last.End {
			if f.End > last.End {
				last.End = f.End
			}
			continue
		}
		out = append(out, f)
	}
	return out
}

// Tail redacts the fragments of known values at the edges of s: a stderr tail or
// a streamed chunk can be cut in the middle of a credential, and half a secret is
// still most of one. Only known values ([WithScope]) can be recognised this way.
// Use it on truncated text only; on whole text [Redactor.Text] is exact and does
// not touch words that merely resemble a value.
func (r *Redactor) Tail(s string) string {
	s = r.Text(s)
	if r.scope == nil || s == "" {
		return s
	}
	a, b := r.scope.edges(s)
	if a == 0 && b == len(s) {
		return s
	}
	var out strings.Builder
	if a > 0 {
		out.WriteString(r.marker)
	}
	out.WriteString(s[a:b])
	if b < len(s) {
		out.WriteString(r.marker)
	}
	return out.String()
}
