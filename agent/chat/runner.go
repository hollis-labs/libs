package chat

import (
	"context"
	"encoding/json"
	"iter"

	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
)

// Runner runs turns for one agent. A host implements it.
//
// Rules for a runner, checked by chattest.RunnerConformance:
//   - Whatever run_id you put on an event is replaced by the Service's.
//   - Emit run.start first and exactly one terminal event last.
//   - Close every part you open. Set final on any part you emit whole; the
//     Service's [Finalizer] fills it for parts you streamed.
//   - Do not emit the user's message; the Service does.
//   - Keep your own record of the turn. The conversation stream is not your
//     model history.
type Runner interface {
	// Start begins a turn. It returns once the turn is accepted; events
	// arrive on the Run. An error means the turn was not accepted: nothing is
	// published for it.
	Start(ctx context.Context, turn Turn) (Run, error)

	// Info reports what the runner can do and its own reference for a
	// conversation. The Service stores RunnerRef on the conversation record.
	Info(ctx context.Context, conversationID string) (Info, error)

	// Record tells the runner about a turn the host answered itself, so the
	// runner's own history includes it. A runner without history ignores it.
	Record(ctx context.Context, turn Turn, answer string) error

	// Replaced yields a conversation ID each time the runner's side of that
	// conversation is replaced (a relaunch, a new session). It is a signal
	// to re-read the generation from Control, not a count. A runner whose
	// side is never replaced returns a channel it never sends on.
	Replaced() <-chan string
}

// Turn is one submitted turn.
type Turn struct {
	ConversationID string
	RunID          string
	Text           string          // the user's input
	Context        json.RawMessage // optional, opaque; a runner may ignore it
	Meta           json.RawMessage // optional small object, echoed on the user's message
}

// Run is one turn in progress.
type Run interface {
	// Events yields chatstream events in order, ending after exactly one
	// terminal event. Seq is left zero; the Service's hub assigns it.
	Events() iter.Seq2[chatstream.Event, error]
	// Cancel asks the turn to stop. The terminal event is run.abort. Cancel
	// must not block and may be called more than once.
	Cancel()
	// Answer resolves an approval this run asked for.
	Answer(ctx context.Context, approvalID string, decision Decision) error
}

// Info is what a runner reports about itself and a conversation it serves.
type Info struct {
	Deltas    bool   // false when the runner yields whole messages only
	Approvals bool   // false when the runner never raises approval.request
	RunnerRef string // the runner's own reference for this conversation
}

// Scope is how far an approval answer reaches.
type Scope string

// The approval scopes.
const (
	// ScopeOnce covers this call.
	ScopeOnce Scope = "once"
	// ScopeConversation covers this tool for the rest of the conversation. The
	// runner holds it.
	ScopeConversation Scope = "conversation"
	// ScopeAlways is a standing grant. The Service hands it to the host's
	// [Grants] store before the runner is answered.
	ScopeAlways Scope = "always"
)

// Valid reports whether s is one of the scopes.
func (s Scope) Valid() bool {
	return s == ScopeOnce || s == ScopeConversation || s == ScopeAlways
}

// Reasons a [Decision] carries when nobody answered.
const (
	// ReasonExpired: the approval's expires_at passed with no answer.
	ReasonExpired = "expired"
)

// Decision is the answer to an approval.
type Decision struct {
	// Allow is true for "allow" and false for "deny".
	Allow bool
	// Scope is how far an allow reaches. A deny is always for this call.
	Scope Scope
	// Reason is empty for an answer a controller gave, and [ReasonExpired] for
	// a denial the Service issued because the approval expired.
	Reason string
}

// Intercept sees every turn before it runs. It may change Context and Meta,
// and returns what happens next. It is called without the conversation locked
// and before the turn is admitted, so it can be called for a turn that is then
// refused because another turn won the race: keep its side effects for the
// Answer path.
type Intercept func(ctx context.Context, turn *Turn) Outcome

type outcomeKind int

const (
	outcomeContinue outcomeKind = iota
	outcomeReject
	outcomeAnswer
)

// Outcome is what an [Intercept] decided. It is exactly one of [Continue],
// [Reject] or [Answer]; the zero value is Continue.
type Outcome struct {
	kind    outcomeKind
	code    string
	message string
	text    string
}

// Continue runs the turn with the runner.
func Continue() Outcome { return Outcome{} }

// Reject refuses the turn. HTTP callers get 422 with both values, Go callers a
// [*RejectedError]. Nothing is published: the turn did not happen.
func Reject(code, message string) Outcome {
	return Outcome{kind: outcomeReject, code: code, message: message}
}

// Answer makes the host answer. The runner is not called; it is told
// afterwards through [Runner.Record]. The exchange is a normal run on the
// stream with provider "host".
func Answer(text string) Outcome { return Outcome{kind: outcomeAnswer, text: text} }

// Grant is a standing approval a controller gave with scope "always".
type Grant struct {
	ConversationID string
	Agent          string
	Owner          string
	RunID          string
	ApprovalID     string
	CallID         string
	// Reason and Descriptor are the approval.request's own, unmodified.
	Reason     string
	Descriptor json.RawMessage
}

// Grants persists standing grants. A host supplies it; the runner checks the
// same store before asking again. A host that supplies none does not offer
// scope "always".
type Grants interface {
	Grant(ctx context.Context, g Grant) error
}
