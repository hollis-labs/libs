// Package decide asks a decision provider structured questions about a piece of
// state: yes or no, pick one of a fixed set, or place on ordered levels. Answers
// come back as probabilities. The provider never writes text; the caller owns the
// questions, the thresholds and what is done with an answer.
//
// Decider is the seam. Client implements it over a decision gateway's HTTP
// operation (POST /ai/decide), which routes to a provider such as JEV. Scripted
// implements it for tests.
//
// The confidence helpers measure how concentrated a distribution is. That is
// not the chance an answer is right: thresholds come from evaluating on your own
// data. Scrub removes recognisable credentials from text before it leaves the
// machine.
package decide

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// QuestionType is one of the three judgment shapes.
type QuestionType string

const (
	Noul   QuestionType = "noul"   // yes/no: the probability a statement is true
	Choice QuestionType = "choice" // pick one of a fixed set
	Score  QuestionType = "score"  // place on ordered levels
)

// Option is one choice of a Choice question, with the description the model reads.
type Option struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

// Question is one judgment. Options and Levels are ordered, and order matters to
// a model (it may lean toward the first); a caller that cares rotates them.
type Question struct {
	Key          string       `json:"key"`
	Type         QuestionType `json:"type"`
	Instructions string       `json:"instructions"`
	Options      []Option     `json:"options,omitempty"` // Choice
	Levels       []string     `json:"levels,omitempty"`  // Score, lowest first
	True         string       `json:"true,omitempty"`    // Noul: what yes means
	False        string       `json:"false,omitempty"`   // Noul: what no means
}

// Validate checks a question's shape.
func (q Question) Validate() error {
	if q.Key == "" {
		return errors.New("decide: question without a key")
	}
	switch q.Type {
	case Choice:
		if len(q.Options) < 2 {
			return fmt.Errorf("decide: choice %q needs at least two options", q.Key)
		}
		seen := map[string]bool{}
		for _, o := range q.Options {
			if o.Key == "" || seen[o.Key] {
				return fmt.Errorf("decide: choice %q: option key empty or repeated", q.Key)
			}
			seen[o.Key] = true
		}
	case Score:
		if len(q.Levels) < 2 {
			return fmt.Errorf("decide: score %q needs at least two levels", q.Key)
		}
	case Noul:
	default:
		return fmt.Errorf("decide: question %q has unknown type %q", q.Key, q.Type)
	}
	return nil
}

// Request asks independent questions about one state. Questions never see each
// other's answers.
type Request struct {
	State     any        `json:"state"`
	Questions []Question `json:"questions"`
	// Model is a hint for the gateway's routing; empty lets it choose. Pin a
	// model id to keep an evaluation reproducible.
	Model string `json:"model,omitempty"`
	// Purpose is required: a gateway accounts spend per purpose.
	Purpose  string `json:"purpose"`
	CallerID string `json:"caller_id,omitempty"`
	// OnError is the caller's failure mode for the gateway: "fail_open"
	// (default) or "fail_closed".
	OnError string `json:"on_error,omitempty"`
}

// Answer is the provider's answer to one question.
type Answer struct {
	Type          QuestionType       `json:"type"`
	Choice        string             `json:"choice,omitempty"`        // Choice: the most likely option key
	Noul          float64            `json:"noul,omitempty"`          // Noul: probability of yes
	Score         float64            `json:"score,omitempty"`         // Score: probability-weighted level index
	Probabilities map[string]float64 `json:"probabilities,omitempty"` // Choice: per option key; Score: per level index
	Confidence    float64            `json:"confidence"`
}

// Usage is what a call cost.
type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"estimated_cost_usd,omitempty"`
}

// Response carries the answers by question key.
type Response struct {
	Provider  string            `json:"provider,omitempty"`
	Model     string            `json:"model"`
	Answers   map[string]Answer `json:"answers"`
	Usage     Usage             `json:"usage"`
	Latency   time.Duration     `json:"latency"`
	RequestID string            `json:"request_id,omitempty"`
}

// Decider answers a Request.
type Decider interface {
	Name() string
	Decide(ctx context.Context, req Request) (Response, error)
}

// ErrNoPurpose is returned before any call: a request must carry a purpose.
var ErrNoPurpose = errors.New("decide: a request must carry a purpose (spend is accounted per purpose)")

// Check validates a request before it is sent.
func (r Request) Check() error {
	if r.Purpose == "" {
		return ErrNoPurpose
	}
	if len(r.Questions) == 0 {
		return errors.New("decide: a request needs at least one question")
	}
	keys := map[string]bool{}
	for _, q := range r.Questions {
		if err := q.Validate(); err != nil {
			return err
		}
		if keys[q.Key] {
			return fmt.Errorf("decide: question key %q twice", q.Key)
		}
		keys[q.Key] = true
	}
	return nil
}

// Scripted answers from a function, for tests and evaluation.
type Scripted struct {
	ID string
	Fn func(ctx context.Context, req Request) (Response, error)
}

// Name implements Decider.
func (s Scripted) Name() string {
	if s.ID == "" {
		return "scripted"
	}
	return s.ID
}

// Decide implements Decider.
func (s Scripted) Decide(ctx context.Context, req Request) (Response, error) {
	if err := req.Check(); err != nil {
		return Response{}, err
	}
	return s.Fn(ctx, req)
}
