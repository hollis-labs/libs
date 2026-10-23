package hitlrouting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/hollis-labs/libs/core/util/router"
)

const (
	// catchAllID is the router rule id of the catch-all.
	catchAllID = "catch_all"
	// minPriority is what the catch-all uses so every rule is tried before it; no rule
	// may use it or anything below.
	minPriority = math.MinInt32
	// Router parameter names carrying the two closed enums.
	paramCompletion = "completion"
	paramDelivery   = "delivery"
)

// Subject is what is routed: the facts about one request. Kind is required; the others
// are optional and a rule that names a field the request lacks does not match.
type Subject struct {
	// Kind is the card or request kind (approval, form, notify, ...).
	Kind string
	// Source is the application or agent that asked.
	Source string
	// Tool is the tool an approval is about, when there is one.
	Tool string
	// Namespace is the scope the request belongs to (a workspace, project or environment).
	Namespace string
}

func (s Subject) fields() map[string]string {
	f := map[string]string{"kind": s.Kind}
	for k, v := range map[string]string{"source": s.Source, "tool": s.Tool, "namespace": s.Namespace} {
		if v != "" {
			f[k] = v
		}
	}
	return f
}

// Route is where one request goes and how.
type Route struct {
	Sink       string
	Completion Completion
	Delivery   Delivery
	// RuleID is the rule that matched; "catch_all" for the catch-all.
	RuleID string
	// Decision is the router's ledger-shaped record of why, always set.
	Decision router.Decision
}

// ErrNotRouted is returned (wrapped in a *NotRoutedError) when the router matched but did
// not apply: a policy denied the match or asked for approval. The Route still carries the
// Decision.
var ErrNotRouted = errors.New("hitlrouting: request was not routed")

// NotRoutedError says why a request got no sink.
type NotRoutedError struct{ Outcome router.Outcome }

func (e *NotRoutedError) Error() string {
	return fmt.Sprintf("hitlrouting: request was not routed (%s)", e.Outcome)
}

// Is makes errors.Is(err, ErrNotRouted) true.
func (e *NotRoutedError) Is(target error) bool { return target == ErrNotRouted }

// Router resolves requests against a compiled profile. It is safe for concurrent use.
type Router struct {
	r *router.Router
}

// Compile validates the profile and builds its router. cfg is passed to the underlying
// router unchanged: a Policy, an Escalator and a Snapshot hook are optional and behave as
// documented there.
func Compile(p Profile, cfg router.Config) (*Router, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	set := router.RuleSet{Version: p.Version, Strategy: router.FirstMatch}
	for _, r := range p.Rules {
		set.Rules = append(set.Rules, router.Rule{
			ID: r.ID, Priority: r.Priority, Disabled: r.Disabled, When: conds(r.Match),
			Target: r.Sink, Params: params(r.Completion, r.Delivery), Auto: true,
		})
	}
	// Every Subject has a kind, so this matches whatever the other rules did not.
	set.Rules = append(set.Rules, router.Rule{
		ID: catchAllID, Priority: minPriority,
		When:   []router.Cond{{Field: "kind", Op: router.OpExists}},
		Target: p.CatchAll.Sink, Params: params(p.CatchAll.Completion, p.CatchAll.Delivery), Auto: true,
	})
	rr, err := router.Compile(set, cfg)
	if err != nil {
		return nil, fmt.Errorf("hitlrouting: %w", err)
	}
	return &Router{r: rr}, nil
}

// Hash identifies the exact compiled profile by content.
func (r *Router) Hash() string { return r.r.Hash() }

// Resolve routes one request. id names it in the decision record. It returns a
// *NotRoutedError (with the Route's Decision set) only when a configured policy declined
// the match; without a policy every request with a kind is routed, at worst by the
// catch-all.
func (r *Router) Resolve(ctx context.Context, id string, s Subject) (Route, error) {
	if strings.TrimSpace(s.Kind) == "" {
		return Route{}, errors.New("hitlrouting: subject kind is required")
	}
	d, err := r.r.Route(ctx, router.Subject{ID: id, Fields: s.fields()})
	out := Route{Decision: d}
	if err != nil {
		return out, fmt.Errorf("hitlrouting: %w", err)
	}
	for _, m := range d.Matches {
		if m.Disposition != router.Apply {
			continue
		}
		out.Sink, out.RuleID = m.Target, m.RuleID
		out.Completion, out.Delivery = Completion(m.Params[paramCompletion]), Delivery(m.Params[paramDelivery])
		return out, nil
	}
	return out, &NotRoutedError{Outcome: d.Outcome}
}

func params(c Completion, d Delivery) map[string]string {
	return map[string]string{paramCompletion: string(c), paramDelivery: string(d)}
}

// conds turns a match into router conditions: one value is exact (or a glob when it holds
// "*" or "?"), several are an any-of list.
func conds(m Match) []router.Cond {
	var out []router.Cond
	for _, f := range m.fields() {
		switch {
		case len(f.values) == 0:
		case len(f.values) > 1:
			out = append(out, router.Cond{Field: f.name, Op: router.OpIn, Values: append([]string(nil), f.values...)})
		case strings.ContainsAny(f.values[0], "*?"):
			out = append(out, router.Cond{Field: f.name, Op: router.OpGlob, Value: f.values[0]})
		default:
			out = append(out, router.Cond{Field: f.name, Op: router.OpEq, Value: f.values[0]})
		}
	}
	return out
}
