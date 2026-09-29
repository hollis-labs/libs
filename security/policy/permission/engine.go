package permission

import (
	"context"
	"sync"
	"time"
)

// Decision is the outcome of a permission check.
type Decision string

// The possible Decision values.
const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
	DecisionAsk   Decision = "ask"
)

// Mode controls the overall permission stance for a session or project.
type Mode string

// The known Mode values.
const (
	// ModeDefault prompts for destructive operations and allows the rest.
	ModeDefault Mode = "default"
	// ModeAcceptEdits auto-accepts file edits and prompts for other writes.
	ModeAcceptEdits Mode = "accept-edits"
	// ModePlan is read-only: every non-read-only tool is denied.
	ModePlan Mode = "plan"
	// ModeYolo skips all permission checks, including deny rules.
	ModeYolo Mode = "yolo"
)

// DefaultApprovalTimeout is the approval window a new Engine uses.
//
// A human reading a tool call, deciding, and clicking needs longer than a
// minute, especially for something with a real-world consequence. A short
// window denies legitimate approvals routinely, and the second-order effect
// is worse than the delay: once people learn the gate rejects them for
// thinking too long, they reach for session-scope approvals or yolo mode and
// the gate stops meaning anything. Deny-on-timeout stays; the window just has
// to be humane. A headless consumer that can never answer an Ask should set
// WithApprovalTimeout short.
const DefaultApprovalTimeout = 5 * time.Minute

// CheckResult holds the decision and metadata from a permission check.
type CheckResult struct {
	Decision    Decision
	RequestID   string // reserved for callers that pair an Ask with RequestApproval
	MatchedRule *Rule  // a copy of the rule that matched, if any
	Reason      string // human-readable reason
}

// ToolMeta carries tool metadata used by the permission engine. The host
// populates it from whatever it knows about the tool being invoked.
type ToolMeta struct {
	IsReadOnly    bool
	IsDestructive bool
	// IsFileEdit marks a file-edit tool. ModeAcceptEdits auto-allows these.
	// A tool is also treated as a file edit when its name was registered
	// with WithFileEditTools.
	IsFileEdit bool
}

// Option configures an Engine at construction time.
type Option func(*Engine)

// WithApprovalTimeout sets the approval window (default
// DefaultApprovalTimeout). Non-positive values are ignored.
func WithApprovalTimeout(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.approvalTimeout = d
		}
	}
}

// WithAuditor installs an Auditor that receives an Event for every decision
// and approval lifecycle step. A nil Auditor disables auditing.
func WithAuditor(a Auditor) Option {
	return func(e *Engine) { e.auditor = a }
}

// WithFileEditTools registers tool names that ModeAcceptEdits treats as file
// edits, in addition to any call that sets ToolMeta.IsFileEdit. The default
// set is empty.
func WithFileEditTools(names ...string) Option {
	return func(e *Engine) {
		for _, n := range names {
			e.fileEditTools[n] = struct{}{}
		}
	}
}

// WithRuleStore installs the RuleStore that persists ScopeProject approvals.
// Without one, a ScopeProject response behaves as ScopeOnce.
func WithRuleStore(s RuleStore) Option {
	return func(e *Engine) { e.ruleStore = s }
}

// WithMatcher overrides how rule patterns are matched against tool input.
// See Matcher for the defaults and their limits.
func WithMatcher(m Matcher) Option {
	return func(e *Engine) { e.matcher = m }
}

// Engine evaluates permission rules and manages the approval flow. It is safe
// for concurrent use.
type Engine struct {
	mu            sync.RWMutex
	mode          Mode
	rules         *RuleSet
	sessionGrants map[string]map[string]Decision // sessionID -> toolName -> decision

	pendingApprovals sync.Map // requestID -> *ApprovalRequest
	approvalTimeout  time.Duration

	// Set once by options in NewEngine and read-only afterwards.
	auditor       Auditor
	fileEditTools map[string]struct{}
	ruleStore     RuleStore
	matcher       Matcher
}

// NewEngine creates a permission engine with the given mode and rules. A nil
// rules value means no rules.
func NewEngine(mode Mode, rules *RuleSet, opts ...Option) *Engine {
	if rules == nil {
		rules = &RuleSet{}
	}
	e := &Engine{
		mode:            mode,
		rules:           rules,
		sessionGrants:   make(map[string]map[string]Decision),
		approvalTimeout: DefaultApprovalTimeout,
		fileEditTools:   make(map[string]struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

// SetMode changes the permission mode. Thread-safe.
func (e *Engine) SetMode(mode Mode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = mode
}

// Mode returns the current permission mode.
func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// SetRules replaces the rule set. Thread-safe.
func (e *Engine) SetRules(rules *RuleSet) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = rules
}

// SetApprovalTimeout overrides the approval window (default
// DefaultApprovalTimeout). It affects WaitForApproval calls that start after
// it returns.
func (e *Engine) SetApprovalTimeout(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.approvalTimeout = d
}

// Check evaluates whether a tool invocation is allowed, denied, or needs
// approval. It is the main entry point, called before tool execution.
//
// Precedence, first match wins (pinned by the TestPrecedence tests):
//
//  1. ModeYolo allows everything, including tools a deny rule names.
//  2. ModePlan allows read-only tools and denies the rest.
//  3. A session grant for the tool name allows it. Grants are keyed by tool
//     name only, so an "allow for session" on a tool also overrides later
//     deny and ask rules for that tool.
//  4. Deny rules, then ask rules, then allow rules (first match per tier).
//  5. The mode default, derived from meta.
//
// The Auditor, if any, sees an EventDecision for every call, without the
// tool input.
func (e *Engine) Check(ctx context.Context, sessionID, toolName string, input map[string]any, meta ToolMeta) CheckResult {
	e.mu.RLock()
	mode := e.mode
	rules := e.rules
	granted := e.sessionGrants[sessionID][toolName] == DecisionAllow
	e.mu.RUnlock()

	res := e.decide(mode, rules, granted, toolName, input, meta)
	e.audit(ctx, Event{Kind: EventDecision, SessionID: sessionID, Tool: toolName, Result: res})
	return res
}

func (e *Engine) decide(mode Mode, rules *RuleSet, granted bool, toolName string, input map[string]any, meta ToolMeta) CheckResult {
	if mode == ModeYolo {
		return CheckResult{Decision: DecisionAllow, Reason: "yolo mode active"}
	}

	if mode == ModePlan {
		if !meta.IsReadOnly {
			return CheckResult{Decision: DecisionDeny, Reason: "plan mode — write operations blocked"}
		}
		return CheckResult{Decision: DecisionAllow, Reason: "plan mode — read-only operation allowed"}
	}

	if granted {
		return CheckResult{Decision: DecisionAllow, Reason: "session grant"}
	}

	if rules != nil && len(rules.Rules) > 0 {
		if result := rules.evaluate(&e.matcher, toolName, input); result != nil {
			return *result
		}
	}

	return e.defaultDecision(mode, toolName, meta)
}

// defaultDecision applies mode-based defaults when no rule matches.
func (e *Engine) defaultDecision(mode Mode, toolName string, meta ToolMeta) CheckResult {
	switch mode {
	case ModeAcceptEdits:
		if meta.IsFileEdit || e.isFileEditTool(toolName) {
			return CheckResult{Decision: DecisionAllow, Reason: "accept-edits mode — file edit auto-allowed"}
		}
		if meta.IsDestructive {
			return CheckResult{Decision: DecisionAsk, Reason: "accept-edits mode — destructive operation requires approval"}
		}
		if !meta.IsReadOnly {
			return CheckResult{Decision: DecisionAsk, Reason: "accept-edits mode — non-edit write requires approval"}
		}
		return CheckResult{Decision: DecisionAllow, Reason: "accept-edits mode — read-only operation"}

	default: // ModeDefault and any unrecognized mode
		if meta.IsDestructive {
			return CheckResult{Decision: DecisionAsk, Reason: "default mode — destructive operation requires approval"}
		}
		if meta.IsReadOnly {
			return CheckResult{Decision: DecisionAllow, Reason: "default mode — read-only operation"}
		}
		return CheckResult{Decision: DecisionAllow, Reason: "default mode — non-destructive operation"}
	}
}

func (e *Engine) isFileEditTool(name string) bool {
	_, ok := e.fileEditTools[name]
	return ok
}

// ClearSessionGrants removes all session-level grants for a session. Call it
// when the session ends.
func (e *Engine) ClearSessionGrants(sessionID string) {
	e.mu.Lock()
	delete(e.sessionGrants, sessionID)
	e.mu.Unlock()
	e.audit(context.Background(), Event{Kind: EventGrantsCleared, SessionID: sessionID})
}
