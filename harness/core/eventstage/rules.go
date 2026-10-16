package eventstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// RulesSchema is the schema id of a classify rule document.
const RulesSchema = "eventstage.rules/v1"

// Limits on a rule document. They keep evaluation bounded and linear.
const (
	MaxRules          = 256
	MaxPayloadMatches = 8
	MaxPatternLen     = 256
	MaxLabels         = 32
	MaxLabelLen       = 64
	MaxScanBytes      = 64 << 10
)

// ErrRules is matched by every error [ParseRules] and [RuleSet.Validate]
// return.
var ErrRules = errors.New("eventstage: invalid rule set")

// RuleSet is a versioned classify rule document: data, not code.
//
//	{
//	  "schema": "eventstage.rules/v1",
//	  "id": "default", "version": "2026-10-16.1",
//	  "rules": [{
//	    "id": "task-blocked",
//	    "match": {"kind": ["task.state_changed"],
//	              "payload": [{"path": "/to", "equals": "blocked"}]},
//	    "set": {"severity": "warning", "labels": ["blocked"]}
//	  }],
//	  "tests": [{
//	    "name": "blocked task",
//	    "event": {"kind": "task.state_changed", "payload": {"to": "blocked"}},
//	    "want": {"severity": "warning", "labels": ["blocked"]}
//	  }]
//	}
//
// Rules run in order and every matching rule applies: the first rule that
// sets a category wins (and only when the event has none), severity only ever
// rises, and labels accumulate (sorted, de-duplicated, capped at [MaxLabels]).
// A rule can set only category, severity and labels. Sensitivity, PII,
// visibility and the sanitized flag belong to Sanitize, and unknown fields are
// rejected so a rule cannot try.
//
// A match is the AND of its parts: kind (exact, "prefix.*" or "*"), category,
// min_severity and payload conditions. A payload condition names an RFC 6901
// JSON pointer and exactly one of equals (string, or the JSON text of a
// scalar), in (list of those), matches (RE2 regular expression) or exists.
// Payload conditions see only the sanitized payload.
//
// The document carries its own tests; [RuleSet.SelfTest] runs them. Changing
// what a rule does is a new Version of the document.
type RuleSet struct {
	Schema  string     `json:"schema"`
	ID      string     `json:"id"`
	Version string     `json:"version"`
	Rules   []Rule     `json:"rules"`
	Tests   []RuleTest `json:"tests,omitempty"`

	compiled []compiledRule
}

// Rule is one classify rule.
type Rule struct {
	ID    string `json:"id"`
	Match Match  `json:"match"`
	Set   Set    `json:"set"`
}

// Match selects events. An empty field matches everything.
type Match struct {
	Kind        []string       `json:"kind,omitempty"`
	Category    []string       `json:"category,omitempty"`
	MinSeverity string         `json:"min_severity,omitempty"`
	Payload     []PayloadMatch `json:"payload,omitempty"`
}

// PayloadMatch is one condition on the sanitized payload.
type PayloadMatch struct {
	Path    string   `json:"path"`
	Equals  *string  `json:"equals,omitempty"`
	In      []string `json:"in,omitempty"`
	Matches string   `json:"matches,omitempty"`
	Exists  *bool    `json:"exists,omitempty"`
}

// Set is what a matching rule contributes.
type Set struct {
	Category string   `json:"category,omitempty"`
	Severity string   `json:"severity,omitempty"`
	Labels   []string `json:"labels,omitempty"`
}

// RuleTest is a self-contained case: an input and the fields it must yield.
type RuleTest struct {
	Name  string          `json:"name"`
	Event RuleTestEvent   `json:"event"`
	Want  RuleTestOutcome `json:"want"`
}

// RuleTestEvent is the input to a [RuleTest].
type RuleTestEvent struct {
	Kind     string          `json:"kind"`
	Category string          `json:"category,omitempty"`
	Severity string          `json:"severity,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// RuleTestOutcome is the expected result of a [RuleTest]. Category and
// Severity are compared when non-empty; Labels is always compared as a set.
type RuleTestOutcome struct {
	Category string   `json:"category,omitempty"`
	Severity string   `json:"severity,omitempty"`
	Labels   []string `json:"labels,omitempty"`
}

// Fields are the classification fields rules read and write.
type Fields struct {
	Category string
	Severity string
	Labels   []string
}

type compiledRule struct {
	kinds []string
	cats  map[string]bool
	minSv int
	conds []compiledCond
}

type compiledCond struct {
	path    []string
	equals  *string
	in      map[string]bool
	matches *regexp.Regexp
	exists  *bool
}

var severityRank = map[string]int{"debug": 0, "info": 1, "notice": 2, "warning": 3, "error": 4, "critical": 5}

var categories = map[string]bool{
	"activity": true, "state": true, "progress": true, "data": true, "message": true,
	"tool": true, "approval": true, "audit": true, "alert": true, "fleet": true, "step": true,
}

// ParseRules decodes and validates a rule document. Unknown fields are an
// error.
func ParseRules(data []byte) (*RuleSet, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rs RuleSet
	if err := dec.Decode(&rs); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRules, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data", ErrRules)
	}
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	return &rs, nil
}

// Validate checks the document and compiles it for evaluation.
func (rs *RuleSet) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrRules, fmt.Sprintf(format, a...))
	}
	if rs.Schema != RulesSchema {
		return bad("schema %q, want %q", rs.Schema, RulesSchema)
	}
	if rs.ID == "" || rs.Version == "" {
		return bad("id and version are required")
	}
	if len(rs.Rules) > MaxRules {
		return bad("%d rules, max %d", len(rs.Rules), MaxRules)
	}
	seen := map[string]bool{}
	rs.compiled = make([]compiledRule, 0, len(rs.Rules))
	for i, r := range rs.Rules {
		if r.ID == "" || seen[r.ID] {
			return bad("rule %d: id %q is empty or duplicate", i, r.ID)
		}
		seen[r.ID] = true
		cr, err := compileRule(r)
		if err != nil {
			return bad("rule %q: %v", r.ID, err)
		}
		rs.compiled = append(rs.compiled, cr)
	}
	for _, t := range rs.Tests {
		if t.Name == "" || t.Event.Kind == "" {
			return bad("every test needs a name and an event kind")
		}
	}
	return nil
}

func compileRule(r Rule) (compiledRule, error) {
	var cr compiledRule
	for _, k := range r.Match.Kind {
		if k == "" || strings.Contains(strings.TrimSuffix(k, ".*"), "*") && k != "*" {
			return cr, fmt.Errorf("kind pattern %q: use an exact kind, \"prefix.*\" or \"*\"", k)
		}
		cr.kinds = append(cr.kinds, k)
	}
	if len(r.Match.Category) > 0 {
		cr.cats = map[string]bool{}
		for _, c := range r.Match.Category {
			if !categories[c] {
				return cr, fmt.Errorf("unknown category %q", c)
			}
			cr.cats[c] = true
		}
	}
	cr.minSv = -1
	if r.Match.MinSeverity != "" {
		n, ok := severityRank[r.Match.MinSeverity]
		if !ok {
			return cr, fmt.Errorf("unknown min_severity %q", r.Match.MinSeverity)
		}
		cr.minSv = n
	}
	if len(r.Match.Payload) > MaxPayloadMatches {
		return cr, fmt.Errorf("%d payload conditions, max %d", len(r.Match.Payload), MaxPayloadMatches)
	}
	for _, p := range r.Match.Payload {
		c, err := compileCond(p)
		if err != nil {
			return cr, err
		}
		cr.conds = append(cr.conds, c)
	}
	if r.Set.Category != "" && !categories[r.Set.Category] {
		return cr, fmt.Errorf("unknown set.category %q", r.Set.Category)
	}
	if r.Set.Severity != "" {
		if _, ok := severityRank[r.Set.Severity]; !ok {
			return cr, fmt.Errorf("unknown set.severity %q", r.Set.Severity)
		}
	}
	if len(r.Set.Labels) > MaxLabels {
		return cr, fmt.Errorf("%d labels, max %d", len(r.Set.Labels), MaxLabels)
	}
	for _, l := range r.Set.Labels {
		if l == "" || len(l) > MaxLabelLen || strings.ContainsAny(l, " \t\r\n,") {
			return cr, fmt.Errorf("label %q: 1-%d characters, no spaces or commas", l, MaxLabelLen)
		}
	}
	if r.Set.Category == "" && r.Set.Severity == "" && len(r.Set.Labels) == 0 {
		return cr, errors.New("set is empty")
	}
	return cr, nil
}

func compileCond(p PayloadMatch) (compiledCond, error) {
	var c compiledCond
	if p.Path != "" && p.Path[0] != '/' {
		return c, fmt.Errorf("path %q is not a JSON pointer", p.Path)
	}
	ops := 0
	if p.Equals != nil {
		ops++
		c.equals = p.Equals
	}
	if p.In != nil {
		ops++
		c.in = map[string]bool{}
		for _, v := range p.In {
			c.in[v] = true
		}
	}
	if p.Matches != "" {
		ops++
		if len(p.Matches) > MaxPatternLen {
			return c, fmt.Errorf("pattern longer than %d", MaxPatternLen)
		}
		re, err := regexp.Compile(p.Matches)
		if err != nil {
			return c, fmt.Errorf("pattern: %v", err)
		}
		c.matches = re
	}
	if p.Exists != nil {
		ops++
		c.exists = p.Exists
	}
	if ops != 1 {
		return c, fmt.Errorf("path %q needs exactly one of equals, in, matches, exists", p.Path)
	}
	for _, seg := range strings.Split(strings.TrimPrefix(p.Path, "/"), "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		c.path = append(c.path, seg)
	}
	if p.Path == "" {
		c.path = nil
	}
	return c, nil
}

// Evaluate applies the rules to an event described by its kind, current
// fields and sanitized payload (nil when the payload must not be inspected).
// It returns the new fields and the ids of the rules that matched.
func (rs *RuleSet) Evaluate(kind string, in Fields, payload []byte) (Fields, []string) {
	var doc any
	if len(payload) > 0 && len(payload) <= MaxScanBytes {
		if err := json.Unmarshal(payload, &doc); err != nil {
			doc = nil
		}
	}
	out := in
	labels := map[string]bool{}
	for _, l := range in.Labels {
		labels[l] = true
	}
	sv, hasSv := severityRank[in.Severity]
	if !hasSv {
		sv = severityRank["info"]
	}
	var matched []string
	for i, cr := range rs.compiled {
		if !cr.matches(kind, in.Category, sv, doc) {
			continue
		}
		r := rs.Rules[i]
		matched = append(matched, r.ID)
		if out.Category == "" && r.Set.Category != "" {
			out.Category = r.Set.Category
		}
		if r.Set.Severity != "" && severityRank[r.Set.Severity] > severityRank[orInfo(out.Severity)] {
			out.Severity = r.Set.Severity
		}
		for _, l := range r.Set.Labels {
			labels[l] = true
		}
	}
	out.Labels = out.Labels[:0:0]
	for l := range labels {
		out.Labels = append(out.Labels, l)
	}
	sort.Strings(out.Labels)
	if len(out.Labels) > MaxLabels {
		out.Labels = out.Labels[:MaxLabels]
	}
	if len(out.Labels) == 0 {
		out.Labels = nil
	}
	return out, matched
}

func orInfo(s string) string {
	if _, ok := severityRank[s]; ok {
		return s
	}
	return "info"
}

func (cr compiledRule) matches(kind, category string, sv int, doc any) bool {
	if len(cr.kinds) > 0 && !kindMatches(cr.kinds, kind) {
		return false
	}
	if cr.cats != nil && !cr.cats[category] {
		return false
	}
	if cr.minSv >= 0 && sv < cr.minSv {
		return false
	}
	for _, c := range cr.conds {
		if !c.holds(doc) {
			return false
		}
	}
	return true
}

func kindMatches(patterns []string, kind string) bool {
	for _, p := range patterns {
		switch {
		case p == "*", p == kind:
			return true
		case strings.HasSuffix(p, ".*") && strings.HasPrefix(kind, strings.TrimSuffix(p, "*")):
			return true
		}
	}
	return false
}

func (c compiledCond) holds(doc any) bool {
	v, found := lookup(doc, c.path)
	if c.exists != nil {
		return found == *c.exists
	}
	if !found {
		return false
	}
	s, ok := scalarText(v)
	if !ok {
		return false
	}
	switch {
	case c.equals != nil:
		return s == *c.equals
	case c.in != nil:
		return c.in[s]
	case c.matches != nil:
		return c.matches.MatchString(s)
	}
	return false
}

func lookup(doc any, path []string) (any, bool) {
	cur := doc
	if cur == nil {
		return nil, false
	}
	for _, seg := range path {
		switch t := cur.(type) {
		case map[string]any:
			next, ok := t[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func scalarText(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	}
	return "", false
}

// SelfTest runs the tests the document carries and returns one error per
// failing case.
func (rs *RuleSet) SelfTest(context.Context) []error {
	var errs []error
	for _, t := range rs.Tests {
		got, _ := rs.Evaluate(t.Event.Kind, Fields{Category: t.Event.Category, Severity: t.Event.Severity}, t.Event.Payload)
		want := append([]string(nil), t.Want.Labels...)
		sort.Strings(want)
		if (t.Want.Category != "" && got.Category != t.Want.Category) ||
			(t.Want.Severity != "" && got.Severity != t.Want.Severity) ||
			strings.Join(got.Labels, ",") != strings.Join(want, ",") {
			errs = append(errs, fmt.Errorf("test %q: got category=%q severity=%q labels=%v, want category=%q severity=%q labels=%v",
				t.Name, got.Category, got.Severity, got.Labels, t.Want.Category, t.Want.Severity, want))
		}
	}
	return errs
}
