package hitltest

import (
	"slices"
	"testing"

	"github.com/hollis-labs/go-hitl"
)

// The brief lists these scenarios; each must exist under its name.
var required = []string{
	"enqueue-idempotent-replay", "idempotency-conflict", "idempotency-scope-per-application",
	"get-pending", "get-terminal", "await-timeout", "await-terminal", "await-out-of-range",
	"withdraw-then-withdraw", "resolve-then-withdraw", "withdraw-then-participant-resolve",
	"withdraw-stale-expected-revision", "terminal-immutability", "caller-isolation",
	"expiry-refuses-late-respond", "expiry-refuses-late-withdraw", "expiry-sweep",
}

func TestRequiredScenariosExist(t *testing.T) {
	var have []string
	for _, s := range Scenarios() {
		have = append(have, s.Name)
	}
	for _, name := range required {
		if !slices.Contains(have, name) {
			t.Errorf("missing scenario %q", name)
		}
	}
	if len(have) != len(required) {
		t.Errorf("%d scenarios, %d required: keep the two lists in step", len(have), len(required))
	}
}

// Scenario self-check: the files are well formed before any adapter runs.
func TestScenariosAreWellFormed(t *testing.T) {
	ops := []string{"enqueue", "get", "await", "withdraw", "resolve", "expire"}
	caps := []Capability{CapExpiry, CapCallerIsolation}
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			if sc.Description == "" || sc.TangentEvidence == "" || len(sc.Steps) == 0 {
				t.Fatal("scenario needs a description, tangent_evidence and steps")
			}
			for _, c := range sc.Requires {
				if !slices.Contains(caps, c) {
					t.Errorf("unknown capability %q", c)
				}
			}
			defined := map[string]bool{}
			for i, st := range sc.Steps {
				if !slices.Contains(ops, st.Op) {
					t.Fatalf("step %d: unknown op %q", i+1, st.Op)
				}
				for _, ref := range []string{st.Item, st.Expect.SameItemAs, st.Expect.DistinctItemFrom, st.Expect.ExistingItemIDIs,
					st.Expect.OutcomeEquals, st.Expect.RevisionSameAs, st.Expect.RevisionGreaterThn} {
					if ref != "" && !defined[ref] {
						t.Errorf("step %d: refers to %q before any step defines it", i+1, ref)
					}
				}
				if st.Op != "enqueue" && st.Op != "expire" && st.Item == "" {
					t.Errorf("step %d: %s needs an item", i+1, st.Op)
				}
				if st.Op == "enqueue" && len(st.Doc) == 0 {
					t.Errorf("step %d: enqueue needs a doc", i+1)
				}
				if st.Op == "resolve" && len(st.Response) == 0 {
					t.Errorf("step %d: resolve needs a response", i+1)
				}
				states := append([]string{st.Expect.State, st.Expect.OutcomeState}, st.Expect.StateIn...)
				for _, s := range states {
					if s != "" && !hitl.State(s).Valid() {
						t.Errorf("step %d: %q is not a state", i+1, s)
					}
				}
				if st.Expect.OutcomeCause != "" && !hitl.CancelCause(st.Expect.OutcomeCause).Valid() {
					t.Errorf("step %d: %q is not a cancel cause", i+1, st.Expect.OutcomeCause)
				}
				if st.As != "" {
					defined[st.As] = true
				}
			}
		})
	}
}
