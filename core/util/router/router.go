package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Subject is what gets routed. Fields are flat dotted keys, for example
// "source", "type", "entity.kind", "entity.value", "mime", "action".
// Confidence is the upstream classifier's confidence in the subject; nil means
// unknown, which is not the same as zero.
type Subject struct {
	ID         string            `json:"id"`
	Fields     map[string]string `json:"fields"`
	Confidence *float64          `json:"confidence,omitempty"`
}

// Op is a condition operator.
type Op string

const (
	// OpEq holds when the field equals Value.
	OpEq Op = "eq"
	// OpNotEq holds when the field is present and differs from Value.
	OpNotEq Op = "not_eq"
	// OpIn holds when the field equals one of Values.
	OpIn Op = "in"
	// OpPrefix holds when the field starts with Value.
	OpPrefix Op = "prefix"
	OpGlob   Op = "glob" // * any run of characters (including "/"), ? one character
	// OpExists holds when the field is present.
	OpExists Op = "exists"
)

// Cond is one test on one field. All conditions of a rule must hold.
type Cond struct {
	Field  string   `json:"field"`
	Op     Op       `json:"op"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

// Disposition says what happens to a match.
type Disposition string

const (
	// Apply routes the match.
	Apply   Disposition = "apply"
	Ask     Disposition = "ask"     // a human decides
	Deny    Disposition = "deny"    // do not route
	Abstain Disposition = "abstain" // as if the rule did not match; let others or escalation try
)

func (d Disposition) rank() int {
	switch d {
	case Deny:
		return 3
	case Ask:
		return 2
	case Apply:
		return 1
	}
	return 0
}

// Rule maps matching subjects to a target.
type Rule struct {
	ID       string            `json:"id"`
	Priority int               `json:"priority,omitempty"` // higher first; ties keep declaration order
	When     []Cond            `json:"when"`
	Target   string            `json:"target"`
	Params   map[string]string `json:"params,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	// Auto false means a match is only a suggestion: disposition is Ask.
	Auto bool `json:"auto,omitempty"`
	// MinConfidence gates the rule. Below it, or unknown, OnLowConfidence
	// decides (default Ask).
	MinConfidence   *float64    `json:"min_confidence,omitempty"`
	OnLowConfidence Disposition `json:"on_low_confidence,omitempty"`
}

// Strategy says how many matching rules are returned.
type Strategy string

const (
	// FirstMatch stops at the first rule that yields a non-abstain match.
	FirstMatch Strategy = "first_match"
	// FanOut returns every matching rule.
	FanOut Strategy = "fan_out"
)

// RuleSet is the declarative input. Strategy has no default: first-match and
// fan-out are both common, and assuming one silently is a bug waiting to happen.
type RuleSet struct {
	Version  string   `json:"version"`
	Strategy Strategy `json:"strategy"`
	Rules    []Rule   `json:"rules"`
}

// ParseRuleSet decodes JSON strictly (unknown fields are errors).
func ParseRuleSet(b []byte) (RuleSet, error) {
	var rs RuleSet
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rs); err != nil {
		return RuleSet{}, err
	}
	return rs, nil
}

// ObligationKind mirrors the policy vocabulary (security/policy). The router
// cannot import it (core may not import security), so the kinds are repeated
// here and an adapter converts. A kind not listed is unknown, and unknown fails
// closed.
type ObligationKind string

const (
	// ObApproval requires an approval before the match is acted on.
	ObApproval ObligationKind = "approval"
	// ObDryRunOnly forbids a real effect.
	ObDryRunOnly ObligationKind = "dry_run_only"
	// ObRateLimit carries a limit the host must enforce.
	ObRateLimit ObligationKind = "rate_limit"
)

// Limit is policy intent in host-defined units; the router never counts.
type Limit struct {
	Count  int64
	Window time.Duration
	Unit   string
}

// Obligation is something the host must do for the match to be acted on. The
// router carries it and applies the one rule it can: approval makes a match Ask.
type Obligation struct {
	Kind      ObligationKind
	PolicyID  string
	Reference string
	Limit     *Limit
}

// PolicyRef is safe provenance: which policy said what.
type PolicyRef struct {
	ID     string
	Source string
	Reason string
}

// Verdict is a policy decision on one candidate match.
type Verdict struct {
	Effect      Disposition // Apply, Ask or Deny; Abstain is treated as Apply
	Obligations []Obligation
	Matched     []PolicyRef
	Reason      string
	// WouldBlock is set when the policy layer was in its own shadow mode and
	// the effect it reports was not enforced.
	WouldBlock bool
	// Failure is a fixed classification (never error text) when the policy
	// layer failed; empty otherwise.
	Failure string
}

// Policy is the policy decision point seam (libs security/policy fits behind it).
type Policy interface {
	Evaluate(ctx context.Context, s Subject, m Match) (Verdict, error)
}

// Escalation is what a later stage did when rules abstained.
type Escalation struct {
	Stage  string `json:"stage"` // classifier | workflow | human
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Escalator is called when no rule produced an applicable-or-asked match.
type Escalator interface {
	Escalate(ctx context.Context, s Subject, d Decision) (Escalation, error)
}

// Config wires the router's optional collaborators.
type Config struct {
	Policy Policy
	// PolicyShadow records policy verdicts but does not let them change dispositions.
	PolicyShadow bool
	Escalator    Escalator
	// Snapshot shapes the Subject copy stored in Decision.Input: redact, drop
	// fields, or return an empty Subject to keep only the ID and a pointer.
	// Nil keeps the subject as given. Retention for raw input is the ledger's
	// policy; this is where it is enforced for routing.
	Snapshot func(Subject) Subject
}

// Router evaluates a compiled rule set. It is safe for concurrent use.
type Router struct {
	set  RuleSet
	hash string
	cfg  Config
	rs   []rule
}

type rule struct {
	Rule
	globs map[int]struct{}
}

// Compile validates and freezes a rule set.
func Compile(rs RuleSet, cfg Config) (*Router, error) {
	var errs []error
	add := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	if rs.Version == "" {
		add("version is required")
	}
	if rs.Strategy != FirstMatch && rs.Strategy != FanOut {
		add("strategy must be %q or %q", FirstMatch, FanOut)
	}
	seen := map[string]bool{}
	for i, r := range rs.Rules {
		if r.ID == "" || seen[r.ID] {
			add("rule #%d: id missing or duplicate (%q)", i, r.ID)
		}
		seen[r.ID] = true
		if r.Target == "" {
			add("rule %q: target required", r.ID)
		}
		if len(r.When) == 0 {
			add("rule %q: empty when would match everything; add a condition or an explicit exists", r.ID)
		}
		for _, c := range r.When {
			if c.Field == "" {
				add("rule %q: condition without field", r.ID)
			}
			switch c.Op {
			case OpEq, OpNotEq, OpPrefix:
				if c.Value == "" {
					add("rule %q: %s on %q needs value", r.ID, c.Op, c.Field)
				}
			case OpIn:
				if len(c.Values) == 0 {
					add("rule %q: in on %q needs values", r.ID, c.Field)
				}
			case OpGlob:
				if c.Value == "" {
					add("rule %q: glob on %q needs value", r.ID, c.Field)
				}
			case OpExists:
			default:
				add("rule %q: unknown op %q", r.ID, c.Op)
			}
		}
		if r.MinConfidence != nil && (*r.MinConfidence < 0 || *r.MinConfidence > 1) {
			add("rule %q: min_confidence outside [0,1]", r.ID)
		}
		switch r.OnLowConfidence {
		case "", Ask, Deny, Abstain:
		default:
			add("rule %q: on_low_confidence must be ask, deny or abstain", r.ID)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	canon, _ := json.Marshal(rs)
	sum := sha256.Sum256(canon)

	ordered := make([]rule, len(rs.Rules))
	for i, r := range rs.Rules {
		ordered[i] = rule{Rule: r}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority > ordered[j].Priority })
	return &Router{set: rs, hash: hex.EncodeToString(sum[:]), cfg: cfg, rs: ordered}, nil
}

// Hash identifies the exact rule set (content, not version label).
func (r *Router) Hash() string { return r.hash }

// Outcome summarizes a decision.
type Outcome string

const (
	Routed    Outcome = "routed"    // at least one Apply
	Suggested Outcome = "suggested" // matches exist but a human decides (Ask only)
	Denied    Outcome = "denied"    // only denials
	NoRoute   Outcome = "no_route"  // nothing matched or all abstained
)

// Match is a rule that applies to the subject.
type Match struct {
	RuleID      string            `json:"rule_id"`
	Target      string            `json:"target"`
	Params      map[string]string `json:"params,omitempty"`
	Disposition Disposition       `json:"disposition"`
	Why         string            `json:"why"`
	Obligations []Obligation      `json:"obligations,omitempty"`
	Policies    []PolicyRef       `json:"policies,omitempty"`
	// DryRun is set by a dry_run_only obligation: the consumer must not cause a
	// real effect.
	DryRun bool `json:"dry_run,omitempty"`
	// PolicyFailure carries a fixed policy failure classification, if any.
	PolicyFailure string `json:"policy_failure,omitempty"`
}

// Considered is one rule's account of itself, kept so a decision can be
// explained and replayed.
type Considered struct {
	RuleID  string `json:"rule_id"`
	Matched bool   `json:"matched"`
	Why     string `json:"why"`
}

// Decision is one ledger row.
type Decision struct {
	Stage       string       `json:"stage"` // always "rule"
	SubjectID   string       `json:"subject_id"`
	Input       Subject      `json:"input"` // snapshot; ledger retention policy decides what to keep
	RuleVersion string       `json:"rule_version"`
	RuleHash    string       `json:"rule_hash"`
	Strategy    Strategy     `json:"strategy"`
	Outcome     Outcome      `json:"outcome"`
	Matches     []Match      `json:"matches,omitempty"`
	Considered  []Considered `json:"alternatives_considered"`
	// ShadowVerdicts holds policy verdicts that were recorded but not applied.
	ShadowVerdicts map[string]Verdict `json:"shadow_verdicts,omitempty"`
	Escalation     *Escalation        `json:"escalation,omitempty"`
}

// Route evaluates the subject. Errors from Policy fail closed for that match
// (Deny); an Escalator error is returned along with the decision so far.
func (r *Router) Route(ctx context.Context, s Subject) (Decision, error) {
	snap := s
	if r.cfg.Snapshot != nil {
		snap = r.cfg.Snapshot(s)
		snap.ID = s.ID
	}
	d := Decision{
		Stage: "rule", SubjectID: s.ID, Input: snap, RuleVersion: r.set.Version, RuleHash: r.hash,
		Strategy: r.set.Strategy, Considered: []Considered{},
	}
	for _, ru := range r.rs {
		if ru.Disabled {
			d.Considered = append(d.Considered, Considered{ru.ID, false, "disabled"})
			continue
		}
		if r.set.Strategy == FirstMatch && len(d.Matches) > 0 {
			d.Considered = append(d.Considered, Considered{ru.ID, false, "not evaluated: first_match already satisfied"})
			continue
		}
		if why, ok := matches(ru.When, s); !ok {
			d.Considered = append(d.Considered, Considered{ru.ID, false, why})
			continue
		}
		disp, why := gate(ru.Rule, s)
		if disp == Abstain {
			d.Considered = append(d.Considered, Considered{ru.ID, false, why})
			continue
		}
		m := Match{RuleID: ru.ID, Target: ru.Target, Params: ru.Params, Disposition: disp, Why: why}
		if r.cfg.Policy != nil {
			v, err := r.cfg.Policy.Evaluate(ctx, s, m)
			if err != nil {
				v = Verdict{Effect: Deny, Reason: "policy error: fail closed", Failure: "policy_error"}
			}
			if r.cfg.PolicyShadow {
				if d.ShadowVerdicts == nil {
					d.ShadowVerdicts = map[string]Verdict{}
				}
				d.ShadowVerdicts[ru.ID] = v
			} else {
				applyVerdict(&m, v)
			}
		}
		d.Matches = append(d.Matches, m)
		d.Considered = append(d.Considered, Considered{ru.ID, true, m.Why})
	}
	d.Outcome = outcome(d.Matches)
	if (d.Outcome == NoRoute) && r.cfg.Escalator != nil {
		esc, err := r.cfg.Escalator.Escalate(ctx, s, d)
		if err != nil {
			return d, fmt.Errorf("escalate: %w", err)
		}
		d.Escalation = &esc
	}
	return d, nil
}

func outcome(ms []Match) Outcome {
	var apply, ask, deny bool
	for _, m := range ms {
		switch m.Disposition {
		case Apply:
			apply = true
		case Ask:
			ask = true
		case Deny:
			deny = true
		}
	}
	switch {
	case apply:
		return Routed
	case ask:
		return Suggested
	case deny:
		return Denied
	}
	return NoRoute
}

// gate applies Auto and the confidence gate. Unknown confidence is not zero:
// it fails a gated rule the same way a low value does, and says so.
func gate(r Rule, s Subject) (Disposition, string) {
	if r.MinConfidence != nil {
		low := r.OnLowConfidence
		if low == "" {
			low = Ask
		}
		switch {
		case s.Confidence == nil:
			return low, fmt.Sprintf("confidence unknown, rule needs >= %.2f -> %s", *r.MinConfidence, low)
		case *s.Confidence < *r.MinConfidence:
			return low, fmt.Sprintf("confidence %.2f < %.2f -> %s", *s.Confidence, *r.MinConfidence, low)
		}
	}
	if !r.Auto {
		return Ask, "matched; rule is not auto"
	}
	return Apply, "matched"
}

func matches(cs []Cond, s Subject) (string, bool) {
	for _, c := range cs {
		v, present := s.Fields[c.Field]
		ok := false
		switch c.Op {
		case OpEq:
			ok = present && v == c.Value
		case OpNotEq:
			ok = present && v != c.Value
		case OpIn:
			for _, x := range c.Values {
				if present && v == x {
					ok = true
				}
			}
		case OpPrefix:
			ok = present && strings.HasPrefix(v, c.Value)
		case OpGlob:
			ok = present && wildcard(c.Value, v)
		case OpExists:
			ok = present
		}
		if !ok {
			return fmt.Sprintf("%s %s failed (field %s)", c.Field, c.Op, presence(present, v)), false
		}
	}
	return "", true
}

func presence(present bool, v string) string {
	if !present {
		return "absent"
	}
	return fmt.Sprintf("= %q", v)
}

// wildcard matches p against s where * is any run (including empty and "/")
// and ? is one rune. path.Match is deliberately not used: its * stops at "/",
// which silently breaks URL and path rules.
func wildcard(p, s string) bool {
	pr, sr := []rune(p), []rune(s)
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(sr) {
		switch {
		case pi < len(pr) && (pr[pi] == '?' || pr[pi] == sr[si]) && pr[pi] != '*':
			pi++
			si++
		case pi < len(pr) && pr[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi, mark = star+1, mark+1
			si = mark
		default:
			return false
		}
	}
	for pi < len(pr) && pr[pi] == '*' {
		pi++
	}
	return pi == len(pr)
}

// applyVerdict folds a policy verdict into a match. Most restrictive wins; a
// verdict can tighten a match, never loosen it. Obligations are the one place
// "allow" is not enough: an unknown kind denies, approval asks, dry-run is
// carried on the match for the consumer to honor.
func applyVerdict(m *Match, v Verdict) {
	eff := v.Effect
	if eff.rank() < Apply.rank() {
		eff = Apply
	}
	var why []string
	for _, o := range v.Obligations {
		switch o.Kind {
		case ObApproval:
			if eff.rank() < Ask.rank() {
				eff = Ask
				why = append(why, "approval required")
			}
		case ObDryRunOnly:
			m.DryRun = true
			why = append(why, "dry run only")
		case ObRateLimit:
			if o.Limit == nil || o.Limit.Count <= 0 || o.Limit.Window <= 0 {
				eff = Deny
				why = append(why, "malformed rate_limit obligation")
			}
		default:
			eff = Deny
			why = append(why, fmt.Sprintf("unknown obligation %q", o.Kind))
		}
	}
	if eff.rank() > m.Disposition.rank() {
		m.Disposition = eff
		if v.Reason != "" {
			why = append([]string{v.Reason}, why...)
		}
		if len(why) > 0 {
			m.Why += "; policy: " + strings.Join(why, ", ")
		}
	}
	m.Obligations = v.Obligations
	m.Policies = v.Matched
	m.PolicyFailure = v.Failure
}
