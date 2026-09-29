package reflexes

// Unified telemetry: every firing goes through EmitFirings immediately after
// Resolve, producing one trace record per fired action.

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"time"
)

// TraceStore is the narrow persistence surface EmitFirings needs: an
// event-log write and the fired-count bump.
type TraceStore interface {
	LogEvent(ctx context.Context, sessionID, eventType, category, detail, metadata string)
	BumpAgentReflexFired(ctx context.Context, id string, now time.Time) error
}

// Filters is the optional plugin seam: filters that may rewrite the State
// before evaluation and each fired action before it is traced, and
// observers told about every firing. Nanite's PluginHooks interface maps
// onto it. It is named Filters, not Hooks, so it does not collide with the
// separate hooks contract (github.com/hollis-labs/go-hooks), which this
// module does not import.
//
// FilterState and FilterAction errors are logged and the unfiltered value
// is kept. Fired and Staged are called once per firing, in that order,
// after the trace write, with the same data map.
type Filters interface {
	FilterState(ctx context.Context, s State) (State, error)
	FilterAction(ctx context.Context, a AppliedAction, meta map[string]any) (AppliedAction, error)
	Fired(sessionID string, data map[string]any)
	Staged(sessionID string, data map[string]any)
}

// FiringContext carries the caller-specific context EmitFirings needs
// beyond what Resolve already computed.
type FiringContext struct {
	// AgentID and AgentClass are folded into every emitted trace record and
	// into the Filters.Fired/Staged payload.
	AgentID    string
	AgentClass string

	// ExtraMetadata, when non-nil, is merged as additional top-level keys
	// into every trace record this call emits, for caller-local audit
	// context. Keys must not collide with the trace record's own field
	// names.
	ExtraMetadata map[string]any
}

// traceRecord is the one consistent shape every reflex firing emits to the
// trace sink. Built once per fired AppliedAction from data Resolve already
// computed; no trigger or combining algorithm is re-evaluated here.
type traceRecord struct {
	ReflexID           string `json:"reflex_id"`
	ReflexName         string `json:"reflex_name"`
	ActionKind         string `json:"action_kind"`
	Category           string `json:"category,omitempty"`
	CombiningAlgorithm string `json:"combining_algorithm,omitempty"`
	// ProvenanceTier comes straight off the fired Reflex.
	ProvenanceTier string `json:"provenance_tier,omitempty"`
	Priority       int64  `json:"priority"`

	AgentID    string `json:"agent_id,omitempty"`
	AgentClass string `json:"agent_class,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	// Attrs is a copy of State.Attrs when the caller populated it.
	Attrs map[string]string `json:"attrs,omitempty"`

	// Spec is the fired action's own action_kind-specific payload
	// (agent_slug/confidence/reason for dispatch_to_agent, body for
	// inject_reminder, tool_name/enforce for force_tool_choice, reason
	// for halt_session, etc.) — deliberately nested rather than
	// flattened to top-level keys, since different kinds carry different
	// fields and flattening them would make "one consistent shape"
	// false advertising.
	Spec map[string]any `json:"spec,omitempty"`

	// AlternativesConsidered lists every other candidate of the winner's
	// action kind, fired or not, when that kind's combining algorithm is
	// first_applicable or deny_overrides. all_applicable has no losers, so
	// it is omitted there.
	AlternativesConsidered []alternativeOutcome `json:"alternatives_considered,omitempty"`
}

// alternativeOutcome is one non-winning competitor's summary inside a
// traceRecord's AlternativesConsidered. CooldownSuppressed separates a
// competitor whose trigger fired but lost to cooldown from one that never
// fired.
type alternativeOutcome struct {
	ReflexID           string `json:"reflex_id"`
	ReflexName         string `json:"reflex_name"`
	Fired              bool   `json:"fired"`
	CooldownSuppressed bool   `json:"cooldown_suppressed,omitempty"`
}

// EmitFirings is the single unified telemetry sink every Resolve caller
// goes through immediately after Resolve returns. For every action in
// applied.Actions it writes one event-log row (event type = the fired
// action kind, category "reflex", detail = the reflex name), bumps that
// reflex's fired count and last-fired time, and, when filters is non-nil,
// calls Fired then Staged. It is a no-op when applied.Actions is empty.
//
// outcomes must be the []CandidateOutcome from the same Resolve call; it is
// used only to build AlternativesConsidered and to recover each winner's
// Category and CombiningAlgorithm, never re-evaluated.
func EmitFirings(
	ctx context.Context,
	ts TraceStore,
	filters Filters,
	applied AppliedActions,
	outcomes []CandidateOutcome,
	state State,
	fc FiringContext,
	logger *slog.Logger,
) {
	emitFirings(ctx, ts, filters, applied, outcomes, state, fc, logger, time.Now())
}

func emitFirings(
	ctx context.Context,
	ts TraceStore,
	hooks Filters,
	applied AppliedActions,
	outcomes []CandidateOutcome,
	state State,
	fc FiringContext,
	logger *slog.Logger,
	now time.Time,
) {
	if len(applied.Actions) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	for i, action := range applied.Actions {
		if i >= len(applied.FiredReflexes) {
			// Defensive only: Actions and FiredReflexes are built 1:1.
			logger.Warn("reflexes.EmitFirings: applied.Actions/FiredReflexes length mismatch, skipping remaining actions",
				"actions", len(applied.Actions), "fired_reflexes", len(applied.FiredReflexes))
			return
		}
		r := applied.FiredReflexes[i]

		var winnerOutcome *CandidateOutcome
		for j := range outcomes {
			if outcomes[j].ReflexID == r.ID && outcomes[j].Selected {
				winnerOutcome = &outcomes[j]
				break
			}
		}

		rec := traceRecord{
			ReflexID:       r.ID,
			ReflexName:     r.Name,
			ActionKind:     action.ActionKind,
			ProvenanceTier: r.ProvenanceTier,
			Priority:       r.Priority,
			AgentID:        fc.AgentID,
			AgentClass:     fc.AgentClass,
			SessionID:      state.SessionID,
			Attrs:          maps.Clone(state.Attrs),
			Spec:           action.Spec,
		}

		algo := ""
		if winnerOutcome != nil {
			rec.Category = winnerOutcome.Category
			rec.CombiningAlgorithm = winnerOutcome.CombiningAlgorithm
			algo = winnerOutcome.CombiningAlgorithm
		}

		// alternatives_considered for kinds under first_applicable or
		// deny_overrides, the two algorithms where "why did this one win" is a
		// meaningful question. Grouped by ActionKind so a candidate whose trigger
		// never fired still shows up as considered-but-not-fired.
		if algo == "first_applicable" || algo == "deny_overrides" {
			for _, cand := range outcomes {
				if cand.ReflexID == r.ID || cand.ActionKind != action.ActionKind {
					continue
				}
				rec.AlternativesConsidered = append(rec.AlternativesConsidered, alternativeOutcome{
					ReflexID:           cand.ReflexID,
					ReflexName:         cand.ReflexName,
					Fired:              cand.TriggerFired,
					CooldownSuppressed: cand.CooldownSuppressed,
				})
			}
		}

		metaJSON, err := json.Marshal(rec)
		if err != nil {
			logger.Warn("reflexes.EmitFirings: marshal trace record failed", "reflex", r.Name, "err", err)
			metaJSON = []byte("{}")
		}
		if len(fc.ExtraMetadata) > 0 {
			var m map[string]any
			if uerr := json.Unmarshal(metaJSON, &m); uerr == nil {
				for k, v := range fc.ExtraMetadata {
					m[k] = v
				}
				if merged, merr := json.Marshal(m); merr == nil {
					metaJSON = merged
				}
			}
		}

		if ts != nil {
			// Outcome bookkeeping must survive cancellation of the reflex it records.
			ts.LogEvent(context.WithoutCancel(ctx), state.SessionID, action.ActionKind, "reflex", r.Name, string(metaJSON))
			if err := ts.BumpAgentReflexFired(ctx, r.ID, now); err != nil {
				logger.Warn("reflexes.EmitFirings: bump fired_count failed",
					"reflex", r.Name, "err", err)
			}
		}

		if hooks != nil {
			data := reflexEventData(fc.AgentID, fc.AgentClass, r, action, state)
			hooks.Fired(state.SessionID, data)
			hooks.Staged(state.SessionID, data)
		}
	}
}

func reflexEventData(agentID, agentClass string, reflex Reflex, action AppliedAction, state State) map[string]any {
	return map[string]any{
		"agent_id":          agentID,
		"agent_class":       agentClass,
		"reflex_id":         reflex.ID,
		"reflex_name":       reflex.Name,
		"trigger_kind":      reflex.TriggerKind,
		"action_kind":       reflex.ActionKind,
		"priority":          reflex.Priority,
		"action":            action,
		"messages":          len(state.Messages),
		"user_messages":     len(state.UserMessages),
		"events":            len(state.Events),
		"mail_unread_count": state.MailUnreadCount,
		"tick_n":            state.TickN,
		"prefix_tokens":     state.PrefixTokens,
	}
}
