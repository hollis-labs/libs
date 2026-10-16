package reflexes

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
)

// ActionKindLookup resolves one ActionKind by name: the combining-algorithm
// lookup Resolve needs per distinct action kind present in a candidate set.
// Engine.Run supplies one backed by its RefreshKinds cache; direct callers
// of Resolve supply their own.
type ActionKindLookup func(ctx context.Context, actionKind string) (*ActionKind, error)

// CooldownFunc reports whether reflex r is currently suppressed by its
// recurrence cooldown (system default -> kind-level override ->
// reflex-level override, resolved via EffectiveCooldown/RecentlyFired,
// recurrence.go). true = suppressed: r's trigger may have fired, but it is
// not eligible to be selected this pass.
type CooldownFunc func(r Reflex) bool

// CandidateOutcome is the per-candidate detail of one Resolve pass: one
// entry per candidate given, whether or not it fired or was selected. It
// distinguishes trigger-false from cooldown-suppressed, records the
// combining algorithm applied and whether the candidate was selected,
// enough to build an alternatives-considered trace without re-evaluating.
type CandidateOutcome struct {
	ReflexID   string `json:"reflex_id"`
	ReflexName string `json:"reflex_name"`
	ActionKind string `json:"action_kind"`
	Priority   int64  `json:"priority"`
	CreatedAt  string `json:"created_at"`

	// TriggerFired is EvaluateTrigger's raw result for this candidate.
	// false both when the trigger genuinely evaluated false and when
	// evaluation errored (see TriggerError) — matching every existing
	// call site's own "an eval error is non-fatal, treat as not fired"
	// handling.
	TriggerFired bool `json:"trigger_fired"`
	// TriggerError carries EvaluateTrigger's error message, if any. Empty
	// on a clean evaluation (fired or not).
	TriggerError string `json:"trigger_error,omitempty"`

	// CooldownSuppressed is true when TriggerFired is true but r's
	// cooldown had not yet elapsed — "not eligible," a fact distinct from
	// "trigger false."
	CooldownSuppressed bool `json:"cooldown_suppressed"`

	// Eligible is true iff TriggerFired && !CooldownSuppressed. Only
	// eligible candidates are grouped by action_kind and run through their
	// kind's combining algorithm.
	Eligible bool `json:"eligible"`

	// CombiningAlgorithm is the ActionKind.CombiningAlgorithm this
	// candidate's action kind resolved to. Empty when the candidate was
	// never eligible (its kind's algorithm was never looked up).
	CombiningAlgorithm string `json:"combining_algorithm,omitempty"`

	// Category is the ActionKind.Category the candidate's kind resolved to.
	// Same availability rule as CombiningAlgorithm, and empty when the
	// lookup failed even though the algorithm still fails open to
	// all_applicable.
	Category string `json:"category,omitempty"`

	// Selected is true iff this candidate's action actually made it into
	// the returned AppliedActions — it survived its kind's combining
	// algorithm (and, for a pass where a deny_overrides kind fired,
	// wasn't preempted by that kind's winner).
	Selected bool `json:"selected"`

	// ApplyError carries Executor.Apply's error message when a selected
	// candidate's action failed to apply — chosen by the combining
	// algorithm, but did not make it into AppliedActions. Matches every
	// existing call site's own "log and skip" handling of an Apply error.
	ApplyError string `json:"apply_error,omitempty"`
}

// Resolve is the one shared decision primitive: given reflex candidates
// already scoped to one evaluation set (the caller's job), a State to
// evaluate triggers against, an Executor to apply selected actions, a
// CooldownFunc and an ActionKindLookup, it decides which fired candidates
// get applied this pass, applies them, and returns the applied actions and
// full per-candidate detail.
//
// Algorithm:
//  1. Evaluate every candidate's trigger (EvaluateTrigger).
//  2. A candidate whose trigger fires is checked against cooldownFn; if
//     suppressed it is "not eligible", distinct from "trigger false"
//     (CandidateOutcome.CooldownSuppressed).
//  3. Eligible candidates are grouped by action kind. Each kind's
//     combining algorithm (via kindLookup) decides selection:
//     - deny_overrides: collected across every deny_overrides kind present
//     this pass; the single highest-priority candidate among that union
//     wins outright (tie-break priority DESC, then CreatedAt ASC) and
//     short-circuits the whole pass, so no other kind's actions apply.
//     - first_applicable: the highest-priority eligible candidate of this
//     kind is selected (same tie-break).
//     - all_applicable: every eligible candidate of this kind is selected.
//  4. Each selected candidate is applied via exec.Apply, in the order the
//     candidates slice was given (expected to be priority DESC, CreatedAt
//     ASC).
//
// A kind lookup failure (kindLookup returns an error, or nil is passed)
// degrades that kind to all_applicable rather than erroring the whole
// pass. Because deny_overrides is exactly what this fails away from, both
// a lookup error and a resolved-but-empty CombiningAlgorithm are
// Warn-logged through the package-level slog logger, naming the action
// kind and the error.
//
// A per-candidate Executor.Apply failure is recorded on that candidate's
// CandidateOutcome.ApplyError and the candidate is not added to
// AppliedActions; it does not abort the rest of the pass. Resolve returns a
// non-nil error only for a caller-programming error (a nil Executor).
func Resolve(
	ctx context.Context,
	candidates []Reflex,
	state State,
	exec *Executor,
	cooldownFn CooldownFunc,
	kindLookup ActionKindLookup,
) (AppliedActions, []CandidateOutcome, error) {
	out := AppliedActions{
		Actions:       make([]AppliedAction, 0),
		FiredReflexes: make([]Reflex, 0),
	}
	outcomes := make([]CandidateOutcome, len(candidates))

	if exec == nil {
		return out, outcomes, fmt.Errorf("reflexes.Resolve: nil Executor")
	}

	// Step 1+2: evaluate every candidate's trigger, then its cooldown
	// eligibility. eligibleByKind preserves the candidates slice's own
	// given order within each kind's bucket.
	eligibleByKind := make(map[string][]int)
	for i, r := range candidates {
		oc := CandidateOutcome{
			ReflexID:   r.ID,
			ReflexName: r.Name,
			ActionKind: r.ActionKind,
			Priority:   r.Priority,
			CreatedAt:  r.CreatedAt,
		}
		fired, evalErr := EvaluateTrigger(r.TriggerKind, r.TriggerSpec, state)
		if evalErr != nil {
			oc.TriggerError = evalErr.Error()
			outcomes[i] = oc
			continue
		}
		oc.TriggerFired = fired
		if !fired {
			outcomes[i] = oc
			continue
		}
		if cooldownFn != nil && cooldownFn(r) {
			oc.CooldownSuppressed = true
			outcomes[i] = oc
			continue
		}
		oc.Eligible = true
		outcomes[i] = oc
		eligibleByKind[r.ActionKind] = append(eligibleByKind[r.ActionKind], i)
	}

	if len(eligibleByKind) == 0 {
		return out, outcomes, nil
	}

	// Resolve each present kind's combining algorithm and category exactly
	// once.
	algoByKind := make(map[string]string, len(eligibleByKind))
	for kind := range eligibleByKind {
		algo := "all_applicable"
		category := ""
		if kindLookup != nil {
			switch k, err := kindLookup(ctx, kind); {
			case err != nil:
				// A lookup error must be operator-visible: it can fail a
				// deny_overrides kind open to all_applicable.
				slog.Warn("reflexes.Resolve: action-kind lookup failed, defaulting combining_algorithm to all_applicable",
					"session_id", state.SessionID,
					"action_kind", kind,
					"err", err,
				)
			case k == nil || k.CombiningAlgorithm == "":
				// Same fail-open, different cause: the lookup succeeded but
				// returned no usable combining algorithm.
				slog.Warn("reflexes.Resolve: action-kind resolved with empty combining_algorithm, defaulting to all_applicable",
					"session_id", state.SessionID,
					"action_kind", kind,
				)
				if k != nil {
					category = k.Category
				}
			default:
				algo = k.CombiningAlgorithm
				category = k.Category
			}
		}
		algoByKind[kind] = algo
		for _, i := range eligibleByKind[kind] {
			outcomes[i].CombiningAlgorithm = algo
			outcomes[i].Category = category
		}
	}

	tieBreakLess := func(a, b Reflex) bool {
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return a.CreatedAt < b.CreatedAt
	}

	// Step 3a: deny_overrides short-circuit — collected across every
	// deny_overrides kind present this pass (only halt_session is seeded
	// as deny_overrides today, but this generalizes correctly if a future
	// kind is too: the single highest-priority candidate among the whole
	// union wins outright and nothing else this pass applies).
	var denyIdx []int
	for kind, idxs := range eligibleByKind {
		if algoByKind[kind] == "deny_overrides" {
			denyIdx = append(denyIdx, idxs...)
		}
	}
	if len(denyIdx) > 0 {
		sort.SliceStable(denyIdx, func(a, b int) bool {
			return tieBreakLess(candidates[denyIdx[a]], candidates[denyIdx[b]])
		})
		winner := denyIdx[0]
		applyOne(ctx, exec, candidates[winner], state, &out, outcomes, winner)
		return out, outcomes, nil
	}

	// Step 3b: no deny_overrides kind fired — first_applicable /
	// all_applicable selection, per kind.
	selected := make(map[int]bool, len(candidates))
	for kind, idxs := range eligibleByKind {
		switch algoByKind[kind] {
		case "first_applicable":
			sorted := append([]int(nil), idxs...)
			sort.SliceStable(sorted, func(a, b int) bool {
				return tieBreakLess(candidates[sorted[a]], candidates[sorted[b]])
			})
			selected[sorted[0]] = true
		default:
			// all_applicable, and any unrecognized/legacy value — fail
			// open the same way an unresolvable kind does above.
			for _, i := range idxs {
				selected[i] = true
			}
		}
	}

	// Step 4: apply every selected candidate, in the candidates slice's
	// own given order (matching every existing call site's prior
	// behavior of applying fired reflexes in priority-then-created_at
	// order regardless of action_kind).
	for i := range candidates {
		if selected[i] {
			applyOne(ctx, exec, candidates[i], state, &out, outcomes, i)
		}
	}
	return out, outcomes, nil
}

// applyOne calls exec.Apply for a selected candidate and records the
// result — either appending to out.Actions/out.FiredReflexes and marking
// outcomes[idx].Selected, or recording outcomes[idx].ApplyError. An apply
// failure is per-candidate and does not propagate as a Resolve() error,
// matching every existing call site's own "log a warning and continue"
// handling of an Executor.Apply failure.
func applyOne(ctx context.Context, exec *Executor, r Reflex, state State, out *AppliedActions, outcomes []CandidateOutcome, idx int) {
	action, err := exec.Apply(ctx, r, state)
	if err != nil {
		outcomes[idx].ApplyError = err.Error()
		return
	}
	outcomes[idx].Selected = true
	out.Actions = append(out.Actions, action)
	out.FiredReflexes = append(out.FiredReflexes, r)
}
