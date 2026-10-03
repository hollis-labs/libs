package reflexes

// Reflex is one steering rule: a trigger (a JSON predicate, event or
// interval spec) evaluated over a State, and an action of a named kind to
// stage or apply when it fires. Field names and JSON tags are identical to
// Nanite's store.AgentReflex so a host can later alias its own type to this
// one. TriggerSpec and ActionSpec are JSON documents held as strings, as
// they are in the source system.
type Reflex struct {
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
	FiredCount  int64  `json:"fired_count"`
	// LastFiredAt is an RFC 3339 timestamp, empty when the reflex has never
	// fired. It feeds the cooldown check (RecentlyFired).
	LastFiredAt string `json:"last_fired_at"`
	// CreatedAt is the tie-break after Priority (earlier wins). It is
	// compared as a string, so use a sortable timestamp format.
	CreatedAt      string `json:"created_at"`
	CreatedBy      string `json:"created_by"`
	OptOutAllowed  bool   `json:"opt_out_allowed"`
	ProvenanceTier string `json:"provenance_tier"`
	// RecurrenceOverrideSeconds is the most specific tier of the cooldown
	// cascade; nil inherits the action kind's default. A pointer to 0 is an
	// explicit "no cooldown".
	RecurrenceOverrideSeconds *int64 `json:"recurrence_override_seconds"`
	WorkflowRunID             string `json:"workflow_run_id"`
}

// ActionKind describes one action kind's facets: its Category, the
// CombiningAlgorithm used when several reflexes of that kind fire in one
// pass (deny_overrides, first_applicable or all_applicable) and its default
// cooldown. Identical field names and tags to Nanite's
// store.ReflexActionKind.
type ActionKind struct {
	Name               string `json:"name"`
	Category           string `json:"category"`
	CombiningAlgorithm string `json:"combining_algorithm"`
	// DefaultRecurrenceSeconds is the kind-level cooldown; nil inherits
	// DefaultReflexCooldown, a pointer to 0 means no cooldown.
	DefaultRecurrenceSeconds *int64 `json:"default_recurrence_seconds"`
}

// MessageSignal is the per-turn signal vector the evaluator inspects. One
// row per message in the recent window, ordered most-recent-first.
//
// The numeric signals (InputTokens, OutputTokens, CacheRead, ToolCalls)
// are plain ints where 0 means "unknown or none". A host that does not
// report cache reads therefore looks like one whose cache reads are 0, and
// a predicate such as cache_read_window = 0 is true for it. See the package
// documentation on conjunctions.
type MessageSignal struct {
	MessageID     string   `json:"message_id"`
	Role          string   `json:"role,omitempty"`
	Content       string   `json:"content"`
	CreatedAt     string   `json:"created_at,omitempty"`
	InputTokens   int      `json:"input_tokens"`
	OutputTokens  int      `json:"output_tokens"`
	CacheRead     int      `json:"cache_read"`
	ToolCalls     int      `json:"tool_calls"`
	ToolNames     []string `json:"tool_names,omitempty"`
	EnvelopeTypes []string `json:"envelope_types,omitempty"`
}

// State is the full snapshot the evaluator consults, built fresh for each
// pass by the caller or a StateSource.
type State struct {
	SessionID  string `json:"session_id"`
	AgentID    string `json:"agent_id"`
	AgentClass string `json:"agent_class"`
	// Messages is most-recent-first. Element 0 is the latest turn.
	Messages []MessageSignal `json:"messages"`
	// UserMessages is most-recent-first. Element 0 is the latest user turn.
	UserMessages []MessageSignal `json:"user_messages,omitempty"`
	// Events is a recent event slice for event-trigger reflexes, ordered
	// most-recent-first.
	Events []EventSignal `json:"events"`
	// MailUnreadCount is the count of unread messages destined for this
	// agent. Non-zero makes the event "mail_received" true.
	MailUnreadCount int `json:"mail_unread_count"`
	// TickN is the current tick counter (0 when unknown). It drives
	// interval triggers.
	TickN int `json:"tick_n"`
	// PrefixTokens is the most recent input prefix size, used by the
	// prefix_pressure predicate. 0 means unknown.
	PrefixTokens int `json:"prefix_tokens"`
	// ScopeTier and ExecutionPattern carry the current turn's live
	// classification, supplied by the caller rather than message history.
	// Empty strings mean the caller has no classification to offer.
	ScopeTier        string `json:"scope_tier,omitempty"`
	ExecutionPattern string `json:"execution_pattern,omitempty"`
	// Attrs carries host-defined string signals for the attr predicate. The
	// library gives no key a special meaning; every entry is copied into the
	// trace record's attrs.
	Attrs map[string]string `json:"attrs,omitempty"`
}

// EventSignal is a thin projection of an event-log entry for the event
// trigger path.
type EventSignal struct {
	EventType string `json:"event_type"`
	Category  string `json:"category"`
	CreatedAt string `json:"created_at"`
}

// AppliedAction describes a single action a reflex requested. The host
// translates these into concrete effects such as a system-reminder
// injection or a tool-choice override.
type AppliedAction struct {
	ReflexID   string         `json:"reflex_id"`
	ReflexName string         `json:"reflex_name"`
	ActionKind string         `json:"action_kind"`
	Spec       map[string]any `json:"spec"`
}

// AppliedActions is the result of a Resolve pass.
type AppliedActions struct {
	Actions []AppliedAction
	// FiredReflexes holds the reflex rows whose actions were selected,
	// index-aligned with Actions.
	FiredReflexes []Reflex
}
