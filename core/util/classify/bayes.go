package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"unicode"
)

// Bayes is a multinomial naive Bayes model over hashed word n-grams for one
// facet. It learns one example at a time, works in log space, and serialises
// to JSON. Its votes are log-posterior differences from the mean, clamped to
// MaxWeight so a long message cannot drown the rules.
type Bayes struct {
	name  string
	facet string
	opts  BayesOptions

	mu       sync.RWMutex
	docs     map[string]float64            // label -> examples learned
	tokens   map[string]float64            // label -> features learned
	counts   map[string]map[uint32]float64 // label -> feature -> count
	vocab    map[uint32]struct{}
	total    float64
	revision uint64 // a running hash over what was learned
}

// BayesOptions tune a Bayes model.
type BayesOptions struct {
	// Buckets is the hashing space; 0 is 1<<18.
	Buckets uint32
	// MinDocs is how many examples it needs before it votes; 0 is 1.
	MinDocs float64
	// MaxWeight clamps each vote; 0 is 3.
	MaxWeight float64
	// Scale multiplies each vote before the clamp; 0 is 1.
	Scale float64
	// Phase is when it may run; empty is Keystroke.
	Phase Phase
}

// NewBayes returns an empty model for facet. name must be unique in an engine.
func NewBayes(name, facet string, o BayesOptions) *Bayes {
	if o.Buckets == 0 {
		o.Buckets = 1 << 18
	}
	if o.MinDocs == 0 {
		o.MinDocs = 1
	}
	if o.MaxWeight == 0 {
		o.MaxWeight = 3
	}
	if o.Scale == 0 {
		o.Scale = 1
	}
	if !o.Phase.Valid() {
		o.Phase = Keystroke
	}
	return &Bayes{name: name, facet: facet, opts: o, docs: map[string]float64{}, tokens: map[string]float64{},
		counts: map[string]map[uint32]float64{}, vocab: map[uint32]struct{}{}}
}

// Name implements Judge.
func (b *Bayes) Name() string { return b.name }

// Phase implements Judge.
func (b *Bayes) Phase() Phase { return b.opts.Phase }

// Version implements Versioned: it changes with every example learned.
func (b *Bayes) Version() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return fmt.Sprintf("%d:%x", int64(b.total), b.revision)
}

// Docs is how many examples the model has learned.
func (b *Bayes) Docs() float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.total
}

// Learn adds one example with the given weight (1 for a plain example; less for
// a noisy, implicit label).
func (b *Bayes) Learn(text, label string, weight float64) {
	if label == "" || weight <= 0 {
		return
	}
	feats := features(text, b.opts.Buckets)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.counts[label] == nil {
		b.counts[label] = map[uint32]float64{}
	}
	for _, f := range feats {
		b.counts[label][f] += weight
		b.vocab[f] = struct{}{}
	}
	b.tokens[label] += weight * float64(len(feats))
	b.docs[label] += weight
	b.total += weight
	h := fnv.New64a()
	fmt.Fprintf(h, "%x|%s|%s|%g", b.revision, label, text, weight)
	b.revision = h.Sum64()
}

// LearnAll adds every example's label for the model's facet.
func (b *Bayes) LearnAll(xs []Example) {
	for _, x := range xs {
		if l := x.Labels[b.facet]; l != "" {
			b.Learn(x.Text, l, 1)
		}
	}
}

// Posterior is the model's own probability per label it has seen, or nil when
// it has not learned enough.
func (b *Bayes) Posterior(text string) map[string]float64 {
	logs := b.logPosterior(text)
	if logs == nil {
		return nil
	}
	maxv := math.Inf(-1)
	for _, v := range logs {
		maxv = math.Max(maxv, v)
	}
	var z float64
	out := make(map[string]float64, len(logs))
	for l, v := range logs {
		out[l] = math.Exp(v - maxv)
		z += out[l]
	}
	for l := range out {
		out[l] /= z
	}
	return out
}

func (b *Bayes) logPosterior(text string) map[string]float64 {
	feats := features(text, b.opts.Buckets)
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.total < b.opts.MinDocs || len(b.docs) < 2 || len(feats) == 0 {
		return nil
	}
	v := float64(len(b.vocab))
	out := make(map[string]float64, len(b.docs))
	for label, n := range b.docs {
		lp := math.Log(n / b.total)
		denom := b.tokens[label] + v
		c := b.counts[label]
		for _, f := range feats {
			lp += math.Log((c[f] + 1) / denom)
		}
		out[label] = lp
	}
	return out
}

// Judge implements Judge.
func (b *Bayes) Judge(_ context.Context, in Input) ([]Vote, error) {
	logs := b.logPosterior(in.Text)
	if logs == nil {
		return nil, nil
	}
	var mean float64
	for _, v := range logs {
		mean += v
	}
	mean /= float64(len(logs))
	out := make([]Vote, 0, len(logs))
	for label, v := range logs {
		w := (v - mean) * b.opts.Scale
		w = math.Max(-b.opts.MaxWeight, math.Min(b.opts.MaxWeight, w))
		out = append(out, Vote{Facet: b.facet, Label: label, Weight: w, Evidence: "bayes"})
	}
	return out, nil
}

type bayesJSON struct {
	Facet    string                        `json:"facet"`
	Buckets  uint32                        `json:"buckets"`
	Docs     map[string]float64            `json:"docs"`
	Tokens   map[string]float64            `json:"tokens"`
	Counts   map[string]map[uint32]float64 `json:"counts"`
	Total    float64                       `json:"total"`
	Revision uint64                        `json:"revision"`
}

// MarshalJSON saves the learned state (not the options other than Buckets).
func (b *Bayes) MarshalJSON() ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return json.Marshal(bayesJSON{b.facet, b.opts.Buckets, b.docs, b.tokens, b.counts, b.total, b.revision})
}

// UnmarshalJSON restores learned state into a model made by NewBayes. The facet
// and bucket count must match.
func (b *Bayes) UnmarshalJSON(raw []byte) error {
	var s bayesJSON
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	if b.counts == nil {
		return errors.New("classify: restore into a model made by NewBayes")
	}
	if s.Facet != b.facet || s.Buckets != b.opts.Buckets {
		return fmt.Errorf("classify: saved model is for facet %q with %d buckets", s.Facet, s.Buckets)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.docs, b.tokens, b.counts, b.total, b.revision = s.Docs, s.Tokens, s.Counts, s.Total, s.Revision
	if b.docs == nil {
		b.docs = map[string]float64{}
	}
	if b.tokens == nil {
		b.tokens = map[string]float64{}
	}
	if b.counts == nil {
		b.counts = map[string]map[uint32]float64{}
	}
	b.vocab = map[uint32]struct{}{}
	for _, c := range b.counts {
		for f := range c {
			b.vocab[f] = struct{}{}
		}
	}
	return nil
}

// features: lower-cased word unigrams and bigrams, the first word marked as
// such (an opening verb or question word carries much of a message's act), and
// a marker for a trailing question mark, hashed into buckets.
func features(text string, buckets uint32) []uint32 {
	words := Tokens(text)
	if len(words) == 0 {
		return nil
	}
	out := make([]uint32, 0, 2*len(words)+2)
	add := func(s string) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(s))
		out = append(out, h.Sum32()%buckets)
	}
	add("^" + words[0])
	for i, w := range words {
		add(w)
		if i > 0 {
			add(words[i-1] + " " + w)
		}
	}
	if strings.HasSuffix(strings.TrimSpace(text), "?") {
		add("<?>")
	}
	return out
}

// Tokens splits text into lower-case words: letters, digits, and the marks
// that hold a word together ('/', '_', '-', '.', '\”) inside it.
func Tokens(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, strings.Trim(cur.String(), "-.'_"))
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(r)
		case (r == '/' || r == '_' || r == '-' || r == '.' || r == '\'') && cur.Len() > 0:
			cur.WriteRune(r)
		case r == '/' && cur.Len() == 0:
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	kept := out[:0]
	for _, w := range out {
		if w != "" {
			kept = append(kept, w)
		}
	}
	return kept
}
