package hitl_test

import (
	"testing"

	"github.com/hollis-labs/substrate/mesh/hitl"
)

// allowed spells out ADR 0001 section 6 independently of CanTransition, one
// line per rule, so the exhaustive check below compares two encodings.
var allowed = map[[2]hitl.State]bool{}

func init() {
	nonterminal := []hitl.State{hitl.StateSubmitted, hitl.StateValidated, hitl.StateStaged, hitl.StatePresented, hitl.StateInProgress}
	add := func(from []hitl.State, to ...hitl.State) {
		for _, f := range from {
			for _, t := range to {
				allowed[[2]hitl.State{f, t}] = true
			}
		}
	}
	// submitted -> validated -> staged -> presented -> in-progress
	add([]hitl.State{hitl.StateSubmitted}, hitl.StateValidated)
	add([]hitl.State{hitl.StateValidated}, hitl.StateStaged)
	add([]hitl.State{hitl.StateStaged}, hitl.StatePresented)
	add([]hitl.State{hitl.StatePresented}, hitl.StateInProgress)
	// presented | in-progress -> resolved
	add([]hitl.State{hitl.StatePresented, hitl.StateInProgress}, hitl.StateResolved)
	// submitted | validated | staged | presented | in-progress -> canceled | expired
	add(nonterminal, hitl.StateCanceled, hitl.StateExpired)
	// submitted | validated | staged -> failed
	add([]hitl.State{hitl.StateSubmitted, hitl.StateValidated, hitl.StateStaged}, hitl.StateFailed)
	// staged | presented | in-progress -> superseded
	add([]hitl.State{hitl.StateStaged, hitl.StatePresented, hitl.StateInProgress}, hitl.StateSuperseded)
}

func TestCanTransitionMatchesADR0001Exhaustively(t *testing.T) {
	states := hitl.AllStates()
	if len(states) != 10 {
		t.Fatalf("AllStates = %d, want 10", len(states))
	}
	for _, from := range states {
		for _, to := range states {
			if got, want := hitl.CanTransition(from, to), allowed[[2]hitl.State{from, to}]; got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTerminalStatesAreExactlyTheFiveAndAreNeverLeft(t *testing.T) {
	var terminal []hitl.State
	for _, s := range hitl.AllStates() {
		if s.IsTerminal() {
			terminal = append(terminal, s)
			for _, to := range hitl.AllStates() {
				if hitl.CanTransition(s, to) {
					t.Errorf("terminal %s can transition to %s", s, to)
				}
			}
		}
	}
	want := []hitl.State{hitl.StateResolved, hitl.StateCanceled, hitl.StateExpired, hitl.StateFailed, hitl.StateSuperseded}
	if len(terminal) != len(want) {
		t.Fatalf("terminal states = %v, want %v", terminal, want)
	}
	for i := range want {
		if terminal[i] != want[i] {
			t.Fatalf("terminal states = %v, want %v", terminal, want)
		}
	}
}

func TestUnknownStateIsNotValidAndCannotTransition(t *testing.T) {
	bogus := hitl.State("closed")
	if bogus.Valid() || bogus.IsTerminal() || hitl.CanTransition(bogus, hitl.StateCanceled) || hitl.CanTransition(hitl.StatePresented, bogus) {
		t.Fatal("an unknown state must be inert")
	}
}
