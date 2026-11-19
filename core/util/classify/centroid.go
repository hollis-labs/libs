package classify

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
)

// Embedder turns texts into vectors. Implementations live outside this package
// (a local ONNX model, a gateway call). Vectors for one Embedder must share a
// dimension.
type Embedder interface {
	Name() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Centroid scores the text against the mean of each label's example vectors.
// Its vote for a label is Scale times the label's cosine minus the mean cosine
// over labels, so it says which label is nearer, not how near.
type Centroid struct {
	name  string
	facet string
	emb   Embedder
	opts  CentroidOptions

	mu        sync.RWMutex
	centroids map[string][]float64
	version   string
}

// CentroidOptions tune a Centroid.
type CentroidOptions struct {
	// Scale converts cosine differences to log-odds; 0 is 20.
	Scale float64
	// MaxWeight clamps each vote; 0 is 4.
	MaxWeight float64
	// Phase is when it may run; empty is Submit.
	Phase Phase
}

// NewCentroid returns a judge over facet. Fit it before use.
func NewCentroid(name, facet string, e Embedder, o CentroidOptions) *Centroid {
	if o.Scale == 0 {
		o.Scale = 20
	}
	if o.MaxWeight == 0 {
		o.MaxWeight = 4
	}
	if !o.Phase.Valid() {
		o.Phase = Submit
	}
	return &Centroid{name: name, facet: facet, emb: e, opts: o}
}

// Name implements Judge.
func (c *Centroid) Name() string { return c.name }

// Phase implements Judge.
func (c *Centroid) Phase() Phase { return c.opts.Phase }

// Version implements Versioned.
func (c *Centroid) Version() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

// Fit embeds the examples for the facet and replaces the centroids. Labels with
// no example get no centroid and no vote.
func (c *Centroid) Fit(ctx context.Context, xs []Example) error {
	var texts, labels []string
	for _, x := range xs {
		if l := x.Labels[c.facet]; l != "" && x.Text != "" {
			texts, labels = append(texts, x.Text), append(labels, l)
		}
	}
	if len(texts) == 0 {
		return errors.New("classify: centroid: no examples for facet " + c.facet)
	}
	vecs, err := c.emb.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("classify: centroid: %w", err)
	}
	if len(vecs) != len(texts) {
		return fmt.Errorf("classify: centroid: %d vectors for %d texts", len(vecs), len(texts))
	}
	sums := map[string][]float64{}
	for i, v := range vecs {
		u := unit(v)
		if u == nil {
			continue
		}
		s := sums[labels[i]]
		if s == nil {
			s = make([]float64, len(u))
		} else if len(s) != len(u) {
			return errors.New("classify: centroid: vectors differ in dimension")
		}
		for k := range u {
			s[k] += u[k]
		}
		sums[labels[i]] = s
	}
	for l, s := range sums {
		sums[l] = unit64(s)
	}
	c.mu.Lock()
	c.centroids = sums
	c.version = fmt.Sprintf("%s:%d", c.emb.Name(), len(texts))
	c.mu.Unlock()
	return nil
}

// Judge implements Judge.
func (c *Centroid) Judge(ctx context.Context, in Input) ([]Vote, error) {
	c.mu.RLock()
	cents := c.centroids
	c.mu.RUnlock()
	if len(cents) < 2 || in.Text == "" {
		return nil, nil
	}
	vecs, err := c.emb.Embed(ctx, []string{in.Text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, errors.New("classify: centroid: embedder returned no vector")
	}
	q := unit(vecs[0])
	if q == nil {
		return nil, nil
	}
	cos := map[string]float64{}
	var mean float64
	for l, cv := range cents {
		if len(cv) != len(q) {
			return nil, errors.New("classify: centroid: vectors differ in dimension")
		}
		var d float64
		for k := range q {
			d += q[k] * cv[k]
		}
		cos[l] = d
		mean += d
	}
	mean /= float64(len(cos))
	out := make([]Vote, 0, len(cos))
	for l, d := range cos {
		w := math.Max(-c.opts.MaxWeight, math.Min(c.opts.MaxWeight, (d-mean)*c.opts.Scale))
		out = append(out, Vote{Facet: c.facet, Label: l, Weight: w, Evidence: fmt.Sprintf("cosine %.3f", d)})
	}
	return out, nil
}

func unit(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return unit64(out)
}

func unit64(v []float64) []float64 {
	var n float64
	for _, x := range v {
		n += x * x
	}
	if n == 0 || math.IsNaN(n) {
		return nil
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] /= n
	}
	return v
}
