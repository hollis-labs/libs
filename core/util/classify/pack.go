package classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Pack is the declarative description of facets, their labels and the rules
// that vote on them.
type Pack struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Facets  []FacetSpec `json:"facets"`
}

// FacetSpec is one facet.
type FacetSpec struct {
	Name  string `json:"name"`
	About string `json:"about,omitempty"`
	// MinP is the probability the top label needs; below it the facet abstains.
	MinP float64 `json:"min_p,omitempty"`
	// MinMargin is how far ahead of the second label the top one must be.
	MinMargin float64 `json:"min_margin,omitempty"`
	// Temperature divides the vote sums before the softmax (calibration); 0 is 1.
	Temperature float64     `json:"temperature,omitempty"`
	Labels      []LabelSpec `json:"labels"`
	Rules       []RuleSpec  `json:"rules,omitempty"`
}

// LabelSpec is one label of a facet.
type LabelSpec struct {
	Name     string   `json:"name"`
	About    string   `json:"about,omitempty"`
	Examples []string `json:"examples,omitempty"`
	// Risk is the consumer's business ("low", "high"); carried, not interpreted.
	Risk string `json:"risk,omitempty"`
	// MinP raises the facet's MinP when this label is on top.
	MinP     float64 `json:"min_p,omitempty"`
	Disabled bool    `json:"disabled,omitempty"`
}

// RuleSpec votes for a label when its context conditions hold and its text test
// (at most one of Pattern, Prefix, Words) matches. A rule with no text test is
// a prior: it votes on the context alone, before anything is typed.
type RuleSpec struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	When  []Cond `json:"when,omitempty"`
	// Pattern is a Go regular expression over the normalised text (lower case,
	// whitespace collapsed).
	Pattern string `json:"pattern,omitempty"`
	// Prefix matches the start of the normalised text.
	Prefix string `json:"prefix,omitempty"`
	// Words matches any of these whole words or phrases.
	Words []string `json:"words,omitempty"`
	// AtStart limits Words to the start of the text.
	AtStart bool `json:"at_start,omitempty"`
	// Weight is the vote in log-odds units; 0 is 1. Negative counts against.
	Weight   float64 `json:"weight,omitempty"`
	Decisive bool    `json:"decisive,omitempty"`
	Disabled bool    `json:"disabled,omitempty"`
}

// Op is a context condition operator.
type Op string

const (
	OpEq     Op = "eq"
	OpNotEq  Op = "not_eq"
	OpIn     Op = "in"
	OpPrefix Op = "prefix"
	OpExists Op = "exists"
	OpAbsent Op = "absent"
)

// Cond is one test on one context field. All of a rule's conditions must hold.
type Cond struct {
	Field  string   `json:"field"`
	Op     Op       `json:"op"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

func (c Cond) holds(ctx map[string]string) bool {
	v, ok := ctx[c.Field]
	switch c.Op {
	case OpEq:
		return ok && v == c.Value
	case OpNotEq:
		return ok && v != c.Value
	case OpIn:
		return ok && slices.Contains(c.Values, v)
	case OpPrefix:
		return ok && strings.HasPrefix(v, c.Value)
	case OpExists:
		return ok && v != ""
	case OpAbsent:
		return !ok || v == ""
	}
	return false
}

// ParsePack decodes JSON strictly: an unknown field is an error.
func ParsePack(b []byte) (Pack, error) {
	var p Pack
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Pack{}, fmt.Errorf("classify: pack: %w", err)
	}
	return p, nil
}

// Layer merges packs, later over earlier: a facet by name (its settings when
// set, its labels by name, its rules by id), so a module pack can add a label,
// replace a rule or disable one. The result takes the last pack's name and
// joins the versions.
func Layer(packs ...Pack) Pack {
	var out Pack
	var versions []string
	for _, p := range packs {
		if p.Name != "" {
			out.Name = p.Name
		}
		if p.Version != "" {
			versions = append(versions, p.Version)
		}
		for _, f := range p.Facets {
			i := slices.IndexFunc(out.Facets, func(x FacetSpec) bool { return x.Name == f.Name })
			if i < 0 {
				out.Facets = append(out.Facets, cloneFacet(f))
				continue
			}
			cur := &out.Facets[i]
			if f.About != "" {
				cur.About = f.About
			}
			if f.MinP != 0 {
				cur.MinP = f.MinP
			}
			if f.MinMargin != 0 {
				cur.MinMargin = f.MinMargin
			}
			if f.Temperature != 0 {
				cur.Temperature = f.Temperature
			}
			for _, l := range f.Labels {
				if j := slices.IndexFunc(cur.Labels, func(x LabelSpec) bool { return x.Name == l.Name }); j >= 0 {
					cur.Labels[j] = l
				} else {
					cur.Labels = append(cur.Labels, l)
				}
			}
			for _, r := range f.Rules {
				if j := slices.IndexFunc(cur.Rules, func(x RuleSpec) bool { return x.ID == r.ID }); j >= 0 {
					cur.Rules[j] = r
				} else {
					cur.Rules = append(cur.Rules, r)
				}
			}
		}
	}
	out.Version = strings.Join(versions, "+")
	return out
}

func cloneFacet(f FacetSpec) FacetSpec {
	f.Labels = slices.Clone(f.Labels)
	f.Rules = slices.Clone(f.Rules)
	return f
}

// Compiled is a validated pack, ready to run.
type Compiled struct {
	Name    string
	Version string
	// Hash is over the pack's content, not its version label.
	Hash   string
	Spec   Pack
	facets map[string]*facet
	order  []*facet
	rules  *ruleJudge
}

type facet struct {
	FacetSpec
}

func (f *facet) has(label string) bool { return f.label(label) != nil }

func (f *facet) label(name string) *LabelSpec {
	for i := range f.Labels {
		if f.Labels[i].Name == name {
			return &f.Labels[i]
		}
	}
	return nil
}

// Compile validates a pack: names present and unique, every rule's label known,
// one text test at most, patterns that compile, thresholds in range. Disabled
// labels and rules are dropped.
func Compile(p Pack) (*Compiled, error) {
	var errs []error
	bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	if p.Name == "" {
		bad("pack name required")
	}
	if len(p.Facets) == 0 {
		bad("pack %q has no facets", p.Name)
	}
	c := &Compiled{Name: p.Name, Version: p.Version, facets: map[string]*facet{}, rules: &ruleJudge{name: "rules:" + p.Name}}
	for _, fs := range p.Facets {
		fs = cloneFacet(fs)
		if fs.Name == "" {
			bad("facet without a name")
			continue
		}
		if c.facets[fs.Name] != nil {
			bad("facet %q twice", fs.Name)
			continue
		}
		if fs.MinP < 0 || fs.MinP > 1 || fs.MinMargin < 0 || fs.MinMargin > 1 || fs.Temperature < 0 {
			bad("facet %q: min_p and min_margin are in [0,1], temperature is not negative", fs.Name)
		}
		fs.Labels = slices.DeleteFunc(fs.Labels, func(l LabelSpec) bool { return l.Disabled })
		fs.Rules = slices.DeleteFunc(fs.Rules, func(r RuleSpec) bool { return r.Disabled })
		if len(fs.Labels) < 2 {
			bad("facet %q needs at least two labels", fs.Name)
		}
		names := map[string]bool{}
		for _, l := range fs.Labels {
			if l.Name == "" || names[l.Name] {
				bad("facet %q: label name empty or repeated (%q)", fs.Name, l.Name)
			}
			if l.MinP < 0 || l.MinP > 1 {
				bad("facet %q label %q: min_p is in [0,1]", fs.Name, l.Name)
			}
			names[l.Name] = true
		}
		ids := map[string]bool{}
		for _, r := range fs.Rules {
			cr, err := compileRule(fs.Name, r, names)
			if err != nil {
				bad("facet %q: %v", fs.Name, err)
				continue
			}
			if ids[r.ID] {
				bad("facet %q: rule id %q twice", fs.Name, r.ID)
			}
			ids[r.ID] = true
			c.rules.rules = append(c.rules.rules, cr)
		}
		f := &facet{fs}
		c.facets[fs.Name] = f
		c.order = append(c.order, f)
		c.Spec.Facets = append(c.Spec.Facets, fs)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("classify: compile: %w", err)
	}
	c.Spec.Name, c.Spec.Version = p.Name, p.Version
	content, err := json.Marshal(c.Spec.Facets)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte(p.Name+"\x00"), content...))
	c.Hash = hex.EncodeToString(sum[:])
	return c, nil
}

// Facets lists the facet specs in pack order.
func (c *Compiled) Facets() []FacetSpec { return slices.Clone(c.Spec.Facets) }

// Facet returns one facet's spec.
func (c *Compiled) Facet(name string) (FacetSpec, bool) {
	f, ok := c.facets[name]
	if !ok {
		return FacetSpec{}, false
	}
	return f.FacetSpec, true
}

// Examples lists a facet's labelled examples, label by label.
func (c *Compiled) Examples(facet string) []Example {
	f, ok := c.facets[facet]
	if !ok {
		return nil
	}
	var out []Example
	for _, l := range f.Labels {
		for _, x := range l.Examples {
			out = append(out, Example{Text: x, Labels: map[string]string{facet: l.Name}})
		}
	}
	return out
}

// Rules is the pack's rule judge (keystroke phase).
func (c *Compiled) Rules() Judge { return c.rules }

type compiledRule struct {
	RuleSpec
	facet string
	re    *regexp.Regexp
	words []*regexp.Regexp
}

func compileRule(facet string, r RuleSpec, labels map[string]bool) (compiledRule, error) {
	cr := compiledRule{RuleSpec: r, facet: facet}
	if r.ID == "" {
		return cr, errors.New("rule without an id")
	}
	if !labels[r.Label] {
		return cr, fmt.Errorf("rule %q: unknown label %q", r.ID, r.Label)
	}
	tests := 0
	for _, set := range []bool{r.Pattern != "", r.Prefix != "", len(r.Words) > 0} {
		if set {
			tests++
		}
	}
	if tests > 1 {
		return cr, fmt.Errorf("rule %q: one of pattern, prefix, words", r.ID)
	}
	if r.AtStart && len(r.Words) == 0 {
		return cr, fmt.Errorf("rule %q: at_start goes with words", r.ID)
	}
	for _, c := range r.When {
		switch c.Op {
		case OpEq, OpNotEq, OpIn, OpPrefix, OpExists, OpAbsent:
		default:
			return cr, fmt.Errorf("rule %q: unknown op %q", r.ID, c.Op)
		}
		if c.Field == "" {
			return cr, fmt.Errorf("rule %q: condition without a field", r.ID)
		}
	}
	if r.Pattern != "" {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return cr, fmt.Errorf("rule %q: %w", r.ID, err)
		}
		cr.re = re
	}
	for _, w := range r.Words {
		w = normalize(w)
		if w == "" {
			return cr, fmt.Errorf("rule %q: empty word", r.ID)
		}
		expr := `\b` + regexp.QuoteMeta(w) + `\b`
		if r.AtStart {
			expr = `^` + expr
		}
		cr.words = append(cr.words, regexp.MustCompile(expr))
	}
	if cr.Weight == 0 {
		cr.Weight = 1
	}
	return cr, nil
}

func (r compiledRule) match(text string, ctx map[string]string) (string, bool) {
	for _, c := range r.When {
		if !c.holds(ctx) {
			return "", false
		}
	}
	switch {
	case r.re != nil:
		m := r.re.FindString(text)
		return m, r.re.MatchString(text)
	case r.Prefix != "":
		return r.Prefix, strings.HasPrefix(text, normalize(r.Prefix))
	case len(r.words) > 0:
		for _, w := range r.words {
			if m := w.FindString(text); m != "" {
				return m, true
			}
		}
		return "", false
	}
	return "context", true
}

type ruleJudge struct {
	name  string
	rules []compiledRule
}

func (j *ruleJudge) Name() string { return j.name }
func (j *ruleJudge) Phase() Phase { return Keystroke }
func (j *ruleJudge) Judge(_ context.Context, in Input) ([]Vote, error) {
	text := normalize(in.Text)
	var out []Vote
	for _, r := range j.rules {
		if (r.re != nil || r.Prefix != "" || len(r.words) > 0) && text == "" {
			continue
		}
		if m, ok := r.match(text, in.Context); ok {
			out = append(out, Vote{Facet: r.facet, Label: r.Label, Weight: r.Weight, Decisive: r.Decisive,
				Evidence: r.ID + ": " + m})
		}
	}
	return out, nil
}
