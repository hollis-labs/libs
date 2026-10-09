package toolselect

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// ErrInvalidOption is returned (wrapped) by Rank for an out-of-range option.
var ErrInvalidOption = errors.New("toolselect: invalid option")

type options struct {
	k1, b      float64
	maxResults int
	stopwords  map[string]struct{}
}

// Option configures a Rank call.
type Option func(*options)

// WithK1B sets the BM25 term-frequency saturation (k1 >= 0) and length
// normalization (0 <= b <= 1) parameters. The defaults are 1.2 and 0.75.
func WithK1B(k1, b float64) Option {
	return func(o *options) { o.k1, o.b = k1, b }
}

// WithMaxResults caps the number of returned hits. 0, the default, means
// unlimited; a negative value is an error.
func WithMaxResults(n int) Option {
	return func(o *options) { o.maxResults = n }
}

// WithStopwords extends, and never replaces, the built-in stopword list. The
// extra words are removed from the query only: the index was built with the
// built-in list alone, so an extra word still counts toward document length.
func WithStopwords(extra ...string) Option {
	return func(o *options) {
		for _, w := range extra {
			for _, t := range tokenize(w) {
				if o.stopwords == nil {
					o.stopwords = map[string]struct{}{}
				}
				o.stopwords[t] = struct{}{}
			}
		}
	}
}

func buildOptions(opts []Option) (options, error) {
	o := options{k1: 1.2, b: 0.75}
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	switch {
	case math.IsNaN(o.k1) || math.IsInf(o.k1, 0) || o.k1 < 0:
		return o, fmt.Errorf("%w: k1 must be finite and >= 0, got %v", ErrInvalidOption, o.k1)
	case math.IsNaN(o.b) || o.b < 0 || o.b > 1:
		return o, fmt.Errorf("%w: b must be in [0,1], got %v", ErrInvalidOption, o.b)
	case o.maxResults < 0:
		return o, fmt.Errorf("%w: max results must be >= 0, got %d", ErrInvalidOption, o.maxResults)
	}
	return o, nil
}

func (o options) isStop(w string) bool {
	if _, ok := builtinStopwords[w]; ok {
		return true
	}
	_, ok := o.stopwords[strings.ToLower(w)]
	return ok
}
