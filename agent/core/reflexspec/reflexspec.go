package reflexspec

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Definition is the authored part of one reflex: its identity, its trigger
// and action, and the flags that steer how a host treats it. Field names and
// JSON tags match go-reflexes' Reflex and Nanite's store.AgentReflex on
// purpose, but this module imports neither and equivalence is not verified
// by running them. Runtime bookkeeping (fired_count, last_fired_at,
// created_at, created_by) is not part of a definition and is left out;
// JSON carrying those fields decodes into a Definition and they are ignored.
//
// TriggerSpec and ActionSpec are JSON documents held as strings, as in the
// source systems.
type Definition struct {
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	ClassTag    string `json:"class_tag"`
	Name        string `json:"name"`
	TriggerKind string `json:"trigger_kind"`
	TriggerSpec string `json:"trigger_spec"`
	ActionKind  string `json:"action_kind"`
	ActionSpec  string `json:"action_spec"`
	Status      string `json:"status"`
	Priority    int64  `json:"priority"`
	// OptOutAllowed is carried, not checked: it is host policy.
	OptOutAllowed bool `json:"opt_out_allowed"`
	// ProvenanceTier is pattern-checked only. Which tier may declare which
	// action kind is host authorization policy and is not this package's.
	ProvenanceTier string `json:"provenance_tier"`
	// RecurrenceOverrideSeconds is the cooldown override: nil inherits the
	// action kind's default, a pointer to 0 is an explicit "no cooldown".
	// A negative value is rejected. A plain int with omitempty would lose
	// the nil/0 distinction, hence the pointer.
	RecurrenceOverrideSeconds *int64 `json:"recurrence_override_seconds"`
	WorkflowRunID             string `json:"workflow_run_id"`
}

// Error codes carried by Error.Code. They are stable identifiers meant for
// programs and for the conformance fixtures; Message is for people.
const (
	// CodeRequired: a required field is missing or empty.
	CodeRequired = "required"
	// CodeInvalidJSON: a spec is not parseable JSON, or has trailing data.
	CodeInvalidJSON = "invalid_json"
	// CodeNotObject: a spec or clause is JSON but not an object.
	CodeNotObject = "not_object"
	// CodeWrongType: a field has the wrong JSON type.
	CodeWrongType = "wrong_type"
	// CodeOutOfRange: a number is outside the range the engine honors.
	CodeOutOfRange = "out_of_range"
	// CodeInvalidValue: a value is not one the grammar accepts (an
	// operator, a mode, a scope, an entity, a malformed identifier).
	CodeInvalidValue = "invalid_value"
	// CodeInvalidRegex: a pattern does not compile as RE2.
	CodeInvalidRegex = "invalid_regex"
	// CodeUnknownTriggerKind: trigger_kind is not predicate, event or
	// interval.
	CodeUnknownTriggerKind = "unknown_trigger_kind"
	// CodeUnknownPredicateKind: a predicate node's kind is not in the
	// grammar. The engine treats this as "never fires".
	CodeUnknownPredicateKind = "unknown_predicate_kind"
	// CodeUnknownField: a trigger node carries a field its kind does not
	// define. The engine ignores such a field, so a typo silently falls
	// back to a default.
	CodeUnknownField = "unknown_field"
	// CodeNotInCatalog: a value failed an injected catalog check.
	CodeNotInCatalog = "not_in_catalog"
	// CodeTooDeep: a predicate tree nests deeper than MaxPredicateDepth.
	CodeTooDeep = "too_deep"
)

// MaxPredicateDepth bounds AND/OR nesting. Deeper trees are rejected rather
// than walked.
const MaxPredicateDepth = 32

// Error is one validation failure. Field is a path into the definition such
// as "trigger_spec.clauses[1].kind" or "action_spec.body".
type Error struct {
	Field   string
	Code    string
	Message string
}

func (e *Error) Error() string {
	return e.Field + ": " + e.Message + " [" + e.Code + "]"
}

func newErr(field, code, format string, args ...any) *Error {
	return &Error{Field: field, Code: code, Message: fmt.Sprintf(format, args...)}
}

// ValidateOption tunes Validate, ValidateTriggerSpec and ValidateActionSpec.
type ValidateOption func(*validateConfig)

type validateConfig struct {
	knownActionKinds     func(string) bool
	knownProvenanceTiers func(string) bool
	knownStatuses        func(string) bool
	knownEvents          func(string) bool
}

// WithKnownActionKinds rejects an action_kind for which known returns false.
// Without it action_kind is only pattern-checked, because a host's action
// kinds are its own (Nanite's are database rows) and a kind this package has
// never heard of is not an error by default. DocumentedActionKinds lists the
// seven kinds go-reflexes and Nanite know about.
func WithKnownActionKinds(known func(string) bool) ValidateOption {
	return func(c *validateConfig) { c.knownActionKinds = known }
}

// WithKnownProvenanceTiers rejects a non-empty provenance_tier for which
// known returns false. Without it the tier is only pattern-checked.
func WithKnownProvenanceTiers(known func(string) bool) ValidateOption {
	return func(c *validateConfig) { c.knownProvenanceTiers = known }
}

// WithKnownStatuses rejects a non-empty status for which known returns
// false. Without it the status is only pattern-checked. Nanite uses
// active, paused and expired.
func WithKnownStatuses(known func(string) bool) ValidateOption {
	return func(c *validateConfig) { c.knownStatuses = known }
}

// WithKnownEvents rejects an event trigger whose name known does not
// recognize. Event names are host-defined, so by default only their shape
// is checked; a misspelled name otherwise never fires.
func WithKnownEvents(known func(string) bool) ValidateOption {
	return func(c *validateConfig) { c.knownEvents = known }
}

func newConfig(opts []ValidateOption) *validateConfig {
	c := &validateConfig{}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	return c
}

// identPattern is the open-enum shape for action kinds, statuses and
// provenance tiers: lowercase words joined by _ . or -.
var identPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// Validate checks the shape of d and returns every problem found, or nil.
// It checks required fields, the trigger_kind grammar, the trigger_spec and
// action_spec documents (see ValidateTriggerSpec and ValidateActionSpec),
// and pattern-checks the open enums. It never touches a store and never
// applies a provenance allow-list. Each returned error is an *Error.
func Validate(d Definition, opts ...ValidateOption) []error {
	cfg := newConfig(opts)
	var errs []error
	if strings.TrimSpace(d.Name) == "" {
		errs = append(errs, newErr("name", CodeRequired, "name is required"))
	}
	if d.TriggerKind == "" {
		errs = append(errs, newErr("trigger_kind", CodeRequired, "trigger_kind is required"))
	}
	// checkTriggerSpec reports an unknown trigger_kind itself.
	errs = append(errs, checkTriggerSpec(d.TriggerKind, d.TriggerSpec, cfg)...)
	errs = append(errs, checkActionKind(d.ActionKind, cfg)...)
	errs = append(errs, checkActionSpec(d.ActionKind, d.ActionSpec)...)
	errs = append(errs, checkOpenEnum("status", d.Status, false, cfg.knownStatuses)...)
	errs = append(errs, checkOpenEnum("provenance_tier", d.ProvenanceTier, false, cfg.knownProvenanceTiers)...)
	if d.RecurrenceOverrideSeconds != nil && *d.RecurrenceOverrideSeconds < 0 {
		errs = append(errs, newErr("recurrence_override_seconds", CodeOutOfRange,
			"recurrence_override_seconds must be >= 0 (0 is an explicit no-cooldown); omit it to inherit"))
	}
	return errs
}

// ValidateTriggerSpec parses triggerSpec according to triggerKind and walks
// it. For "predicate" it checks every node's kind against the grammar and
// each kind's required fields, types and operators; for "event" it checks
// the name; for "interval" every_n_ticks. It returns nil, or the problems
// joined with errors.Join, each an *Error reachable through errors.As.
func ValidateTriggerSpec(triggerKind, triggerSpec string, opts ...ValidateOption) error {
	return errors.Join(checkTriggerSpec(triggerKind, triggerSpec, newConfig(opts))...)
}

// ValidateActionSpec checks actionSpec for actionKind. Every kind requires a
// JSON object. inject_reminder, halt_session, dispatch_to_agent and
// resume_loop_run additionally have the fields evidenced in real seeds and
// code; force_tool_choice, add_schedule, send_message and any other kind get
// the object check only. Unknown extra fields are allowed. WithKnownActionKinds
// is honored; the other options do not apply.
func ValidateActionSpec(actionKind, actionSpec string, opts ...ValidateOption) error {
	cfg := newConfig(opts)
	errs := checkActionKind(actionKind, cfg)
	errs = append(errs, checkActionSpec(actionKind, actionSpec)...)
	return errors.Join(errs...)
}

func checkActionKind(kind string, cfg *validateConfig) []error {
	return checkOpenEnum("action_kind", kind, true, cfg.knownActionKinds)
}

func checkOpenEnum(field, v string, required bool, known func(string) bool) []error {
	if v == "" {
		if required {
			return []error{newErr(field, CodeRequired, "%s is required", field)}
		}
		return nil
	}
	if !identPattern.MatchString(v) {
		return []error{newErr(field, CodeInvalidValue,
			"%s %q must be lowercase words joined by _ . or - (at most 64 characters)", field, v)}
	}
	if known != nil && !known(v) {
		return []error{newErr(field, CodeNotInCatalog, "%s %q is not in the configured catalog", field, v)}
	}
	return nil
}
