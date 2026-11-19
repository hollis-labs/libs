package classify

import (
	"math"
	"sort"
)

// Novelty is split in two on purpose. A nearest-neighbour "novelty" score over
// one pool mostly finds duplicates: near-copies sit far from everything else
// and all look new together, and in high dimensions the score collapses to top-1
// cosine. So:
//
//   - Dedupe answers "have we seen this?": groups of near-copies.
//   - Emerging answers "is something new forming?": among what the classifier
//     could not label (its abstentions), clusters big enough to be a candidate
//     label, counted after near-copies are collapsed, shown as clusters with a
//     representative, never as single items.

// Item is one text with its vector.
type Item struct {
	ID     string    `json:"id"`
	Text   string    `json:"text"`
	Vector []float32 `json:"-"`
}

// Group is a set of items, by index into the slice given, with the item nearest
// the group's centre as its representative.
type Group struct {
	Members        []int  `json:"members"`
	Representative int    `json:"representative"`
	Text           string `json:"text"`
	// Distinct is how many members remain after near-copies are collapsed.
	Distinct int `json:"distinct"`
}

// Dedupe groups near-copies: items whose cosine to a group's first member is at
// least threshold (0 means 0.95). Groups of one are included; order follows the
// items.
func Dedupe(items []Item, threshold float64) []Group {
	if threshold <= 0 {
		threshold = 0.95
	}
	return leaders(items, unitAll(items), threshold, nil)
}

// Emerging clusters items (usually the inputs a facet abstained on) at
// threshold cosine to a cluster's first member (0 means 0.8), after collapsing
// near-copies (cosine ≥ 0.95) so a pasted log repeated forty times counts once.
// It returns the clusters with at least minDistinct distinct members (0 means
// 3), largest first.
func Emerging(items []Item, threshold float64, minDistinct int) []Group {
	if threshold <= 0 {
		threshold = 0.8
	}
	if minDistinct <= 0 {
		minDistinct = 3
	}
	units := unitAll(items)
	dups := leaders(items, units, 0.95, nil)
	// One distinct item per near-copy group: its leader.
	leaderOf := make([]int, len(items))
	var distinct []int
	for _, g := range dups {
		for _, m := range g.Members {
			leaderOf[m] = g.Members[0]
		}
		distinct = append(distinct, g.Members[0])
	}
	clusters := leaders(items, units, threshold, distinct)
	var out []Group
	for _, c := range clusters {
		if c.Distinct < minDistinct {
			continue
		}
		// Bring every near-copy of a distinct member back in.
		in := map[int]bool{}
		for _, m := range c.Members {
			in[m] = true
		}
		var all []int
		for i := range items {
			if in[leaderOf[i]] {
				all = append(all, i)
			}
		}
		c.Members = all
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Distinct > out[j].Distinct })
	return out
}

// leaders is greedy leader clustering over the given indexes (all when nil):
// each item joins the first group whose leader it is close enough to, or leads
// a new one.
func leaders(items []Item, units [][]float64, threshold float64, only []int) []Group {
	if only == nil {
		only = make([]int, len(items))
		for i := range only {
			only[i] = i
		}
	}
	var groups []Group
	for _, i := range only {
		if units[i] == nil {
			continue
		}
		placed := false
		for g := range groups {
			if dot(units[i], units[groups[g].Members[0]]) >= threshold {
				groups[g].Members = append(groups[g].Members, i)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, Group{Members: []int{i}})
		}
	}
	for g := range groups {
		groups[g].Distinct = len(groups[g].Members)
		groups[g].Representative = medoid(groups[g].Members, units)
		groups[g].Text = items[groups[g].Representative].Text
	}
	return groups
}

func medoid(members []int, units [][]float64) int {
	dim := len(units[members[0]])
	centre := make([]float64, dim)
	for _, m := range members {
		for k, x := range units[m] {
			centre[k] += x
		}
	}
	best, bestDot := members[0], math.Inf(-1)
	for _, m := range members {
		if d := dot(units[m], centre); d > bestDot {
			best, bestDot = m, d
		}
	}
	return best
}

func unitAll(items []Item) [][]float64 {
	out := make([][]float64, len(items))
	dim := -1
	for i, it := range items {
		if len(it.Vector) == 0 {
			continue
		}
		if dim < 0 {
			dim = len(it.Vector)
		}
		if len(it.Vector) != dim {
			continue // a vector of another dimension cannot be compared; leave it out
		}
		out[i] = unit(it.Vector)
	}
	return out
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
