package toolselect

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// ErrInvalidCatalog is returned (wrapped) by NewIndex for a catalog with an
// empty or duplicate tool name.
var ErrInvalidCatalog = errors.New("toolselect: invalid catalog")

type doc struct {
	tf  map[string]int
	len int
}

// Index is a Catalog prepared for repeated ranking. It is immutable after
// NewIndex and safe for concurrent use.
type Index struct {
	tools  []Tool
	docs   []doc
	df     map[string]int
	byName map[string]int
	avgdl  float64
}

// NewIndex builds a ranking index over catalog in O(len(catalog.Tools)). The
// catalog is copied, so later changes to it do not affect the index. Each tool
// is indexed as one document made of its name, title, description, tags,
// argument names and argument descriptions.
// An empty or duplicate Tool.Name is an error.
func NewIndex(catalog Catalog) (*Index, error) {
	idx := &Index{
		tools:  make([]Tool, len(catalog.Tools)),
		docs:   make([]doc, len(catalog.Tools)),
		df:     map[string]int{},
		byName: make(map[string]int, len(catalog.Tools)),
	}
	total := 0
	for i, t := range catalog.Tools {
		if t.Name == "" {
			return nil, fmt.Errorf("%w: tool %d has an empty name", ErrInvalidCatalog, i)
		}
		if _, dup := idx.byName[t.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate tool name %q", ErrInvalidCatalog, t.Name)
		}
		idx.byName[t.Name] = i
		t.Tags = slices.Clone(t.Tags)
		t.Arguments = slices.Clone(t.Arguments)
		idx.tools[i] = t

		var text strings.Builder
		text.WriteString(t.Name + " " + t.Title + " " + t.Description + " " + strings.Join(t.Tags, " "))
		for _, argument := range t.Arguments {
			text.WriteByte(' ')
			text.WriteString(argument.Name)
			text.WriteByte(' ')
			text.WriteString(argument.Description)
		}
		d := doc{tf: map[string]int{}}
		for _, tok := range tokenize(text.String()) {
			if _, stop := builtinStopwords[tok]; stop {
				continue
			}
			d.tf[tok]++
			d.len++
		}
		for tok := range d.tf {
			idx.df[tok]++
		}
		idx.docs[i] = d
		total += d.len
	}
	if len(idx.tools) > 0 && total > 0 {
		idx.avgdl = float64(total) / float64(len(idx.tools))
	} else {
		idx.avgdl = 1
	}
	return idx, nil
}

// Rank is the one-shot form of NewIndex followed by [Index.Rank]. Prefer a
// held *Index when ranking the same catalog more than once.
func Rank(catalog Catalog, query string, rules []Rule, opts ...Option) ([]Hit, error) {
	idx, err := NewIndex(catalog)
	if err != nil {
		return nil, err
	}
	return idx.Rank(query, rules, opts...)
}

type cand struct {
	i                    int
	tier                 MatchTier
	score                float64
	exactCase, exactName bool
}

// Rank orders the index's catalog against query:
//
//  1. Rules whose Intent matches the query are applied in priority order
//     (equal priorities in declaration order). Exclude always wins; once an
//     include rule applies, only included tools survive.
//  2. Applicable [ActionOrder] rules pin tools to the front as [TierPinned],
//     in Pin order.
//  3. The rest are tiered as [TierExactName], [TierPrefix] or [TierBM25].
//     Literal name matches lead case-folded names, then title-only matches
//     within TierExactName. BM25 uses Score descending, with Tool.Name as the final
//     tiebreak. Tools sharing no term with the query and not exact or prefix
//     matches are omitted.
//
// BM25 document frequencies are catalog-wide, so a score does not depend on
// which rules apply. Rank is pure: the same index, query, rules and options
// always produce the same result. A malformed glob in a rule matches nothing.
func (idx *Index) Rank(query string, rules []Rule, opts ...Option) ([]Hit, error) {
	o, err := buildOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := validateRules(rules); err != nil {
		return nil, err
	}
	applicable := applicableRules(rules, query)

	// Stage 1: exclude / include.
	n := len(idx.tools)
	excluded := make([]bool, n)
	included := make([]bool, n)
	hasInclude := false
	for _, r := range applicable {
		switch r.Action.Type {
		case ActionExclude:
			for i := range idx.tools {
				if toolMatches(&idx.tools[i], r.Match) {
					excluded[i] = true
				}
			}
		case ActionInclude:
			hasInclude = true
			for i := range idx.tools {
				if toolMatches(&idx.tools[i], r.Match) {
					included[i] = true
				}
			}
		case ActionOrder:
		}
	}
	alive := func(i int) bool { return !excluded[i] && (!hasInclude || included[i]) }

	// Stage 2: pins.
	pinned := make([]bool, n)
	var pins []int
	for _, r := range applicable {
		if r.Action.Type != ActionOrder {
			continue
		}
		for _, name := range r.Action.Pin {
			i, ok := idx.byName[name]
			if !ok || pinned[i] || !alive(i) || !toolMatches(&idx.tools[i], r.Match) {
				continue
			}
			pinned[i] = true
			pins = append(pins, i)
		}
	}

	// Stage 3: tier and score the rest.
	trimmedQuery := strings.TrimSpace(query)
	q := strings.ToLower(trimmedQuery)
	terms := idx.queryTerms(query, o)
	weights := idx.idf(terms)
	var rest []cand
	for i := range idx.tools {
		if !alive(i) || pinned[i] {
			continue
		}
		t := &idx.tools[i]
		c := cand{i: i, tier: TierBM25}
		switch {
		case q != "" && (strings.EqualFold(t.Name, q) || (t.Title != "" && strings.EqualFold(t.Title, q))):
			c.tier = TierExactName
			c.exactCase = t.Name == trimmedQuery
			c.exactName = strings.EqualFold(t.Name, q)
		case q != "" && (strings.HasPrefix(strings.ToLower(t.Name), q) || strings.HasPrefix(q, strings.ToLower(t.Name))):
			c.tier = TierPrefix
		default:
			c.score = idx.bm25(i, terms, weights, o)
			if c.score <= 0 {
				continue
			}
		}
		rest = append(rest, c)
	}
	sort.Slice(rest, func(a, b int) bool {
		x, y := rest[a], rest[b]
		if x.tier != y.tier {
			return x.tier < y.tier
		}
		if x.exactCase != y.exactCase {
			return x.exactCase
		}
		if x.exactName != y.exactName {
			return x.exactName
		}
		if x.score != y.score {
			return x.score > y.score
		}
		return idx.tools[x.i].Name < idx.tools[y.i].Name
	})

	hits := make([]Hit, 0, len(pins)+len(rest))
	for _, i := range pins {
		hits = append(hits, Hit{Tool: idx.cloneTool(i), Tier: TierPinned})
	}
	for _, c := range rest {
		hits = append(hits, Hit{Tool: idx.cloneTool(c.i), Tier: c.tier, Score: c.score})
	}
	if o.maxResults > 0 && len(hits) > o.maxResults {
		hits = hits[:o.maxResults]
	}
	return hits, nil
}

func (idx *Index) cloneTool(i int) Tool {
	t := idx.tools[i]
	t.Tags = slices.Clone(t.Tags)
	t.Arguments = slices.Clone(t.Arguments)
	return t
}

// queryTerms returns the query's distinct non-stopword tokens, sorted so the
// score summation order is fixed.
func (idx *Index) queryTerms(query string, o options) []string {
	seen := map[string]struct{}{}
	var terms []string
	for _, tok := range tokenize(query) {
		if o.isStop(tok) {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		terms = append(terms, tok)
	}
	sort.Strings(terms)
	return terms
}

// idf returns the Lucene-style non-negative inverse document frequency for
// each term, or 0 for a term absent from the corpus.
func (idx *Index) idf(terms []string) []float64 {
	n := float64(len(idx.tools))
	w := make([]float64, len(terms))
	for k, t := range terms {
		df := float64(idx.df[t])
		if df == 0 {
			continue
		}
		w[k] = math.Log(1 + (n-df+0.5)/(df+0.5))
	}
	return w
}

func (idx *Index) bm25(i int, terms []string, weights []float64, o options) float64 {
	d := idx.docs[i]
	norm := o.k1 * (1 - o.b + o.b*float64(d.len)/idx.avgdl)
	score := 0.0
	for k, t := range terms {
		tf := float64(d.tf[t])
		if tf == 0 {
			continue
		}
		score += weights[k] * tf * (o.k1 + 1) / (tf + norm)
	}
	return score
}
