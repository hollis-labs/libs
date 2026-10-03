package hitl

// State is the lifecycle state of one interaction. Spellings are the wire
// spellings of Tangent's frozen contract, including in_progress (ADR 0001
// writes in-progress; the schema and the code write in_progress).
type State string

// Lifecycle states. The last five are terminal.
const (
	StateSubmitted  State = "submitted"
	StateValidated  State = "validated"
	StateStaged     State = "staged"
	StatePresented  State = "presented"
	StateInProgress State = "in_progress"
	StateResolved   State = "resolved"
	StateCanceled   State = "canceled"
	StateExpired    State = "expired"
	StateFailed     State = "failed"
	StateSuperseded State = "superseded"
)

// AllStates lists every state in lifecycle order.
func AllStates() []State {
	return []State{
		StateSubmitted, StateValidated, StateStaged, StatePresented, StateInProgress,
		StateResolved, StateCanceled, StateExpired, StateFailed, StateSuperseded,
	}
}

// Valid reports whether s is one of the ten known states.
func (s State) Valid() bool {
	for _, k := range AllStates() {
		if s == k {
			return true
		}
	}
	return false
}

// IsTerminal reports whether s is resolved, canceled, expired, failed or
// superseded. A terminal state is never left.
func (s State) IsTerminal() bool {
	switch s {
	case StateResolved, StateCanceled, StateExpired, StateFailed, StateSuperseded:
		return true
	case StateSubmitted, StateValidated, StateStaged, StatePresented, StateInProgress:
		return false
	}
	return false
}

// CanTransition reports whether the lifecycle allows moving from one state to
// another (ADR 0001 section 6):
//
//	submitted -> validated -> staged -> presented -> in_progress
//	presented | in_progress -> resolved
//	any nonterminal -> canceled | expired
//	submitted | validated | staged -> failed
//	staged | presented | in_progress -> superseded
//
// Staying in the same state is not a transition.
func CanTransition(from, to State) bool {
	switch from {
	case StateSubmitted:
		return to == StateValidated || to == StateCanceled || to == StateExpired || to == StateFailed
	case StateValidated:
		return to == StateStaged || to == StateCanceled || to == StateExpired || to == StateFailed
	case StateStaged:
		return to == StatePresented || to == StateCanceled || to == StateExpired ||
			to == StateFailed || to == StateSuperseded
	case StatePresented:
		return to == StateInProgress || to == StateResolved || to == StateCanceled ||
			to == StateExpired || to == StateSuperseded
	case StateInProgress:
		return to == StateResolved || to == StateCanceled || to == StateExpired || to == StateSuperseded
	case StateResolved, StateCanceled, StateExpired, StateFailed, StateSuperseded:
		return false
	}
	return false
}
