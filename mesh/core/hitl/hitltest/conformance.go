package hitltest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/go-hitl"
	"github.com/hollis-labs/go-hitl/schema"
)

// RunConformance runs every scenario against a. Scenarios whose Requires
// capabilities are not all in caps are skipped with a message naming what is
// missing, so a skip is never mistaken for a pass.
//
// Beyond each step's Expect, the runner asserts for every response that it
// validates against the schema bundle (success results against the result
// definition, error bodies against HITLErrorV1 or HITLPlainErrorV1) and that
// every observed state change per item is a path that hitl.CanTransition
// allows.
func RunConformance(t *testing.T, a Adapter, caps ...Capability) {
	t.Helper()
	have := map[Capability]bool{}
	for _, c := range caps {
		have[c] = true
	}
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			for _, need := range sc.Requires {
				if !have[need] {
					t.Skipf("SKIPPED (not passed): adapter does not declare capability %q", need)
				}
			}
			runScenario(t, a, sc)
		})
	}
}

// reporter is the slice of *testing.T the runner needs; the package's own
// tests substitute a recorder to prove that broken adapters fail.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

func runScenario(t reporter, a Adapter, sc Scenario) {
	r := &runner{t: t, a: a, run: randomToken(), vars: map[string]*capture{}, observed: map[string]hitl.State{}}
	for i, st := range sc.Steps {
		r.step(i, st)
	}
}

type capture struct {
	itemID   string
	revision int64
	body     map[string]any
}

type runner struct {
	t        reporter
	a        Adapter
	run      string
	vars     map[string]*capture
	observed map[string]hitl.State
}

func randomToken() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (r *runner) subst(raw []byte) []byte {
	s := strings.ReplaceAll(string(raw), "{{run}}", r.run)
	for name, c := range r.vars {
		s = strings.ReplaceAll(s, "${"+name+"}", c.itemID)
	}
	return []byte(s)
}

func (r *runner) itemID(step string, ref string) string {
	c, ok := r.vars[ref]
	if !ok {
		r.t.Fatalf("%s: unknown variable %q", step, ref)
	}
	return c.itemID
}

func (r *runner) command(st Step) []byte {
	caller := st.Caller
	if caller == "" {
		caller = "conf-a"
	}
	cmd := map[string]any{
		"contract_version": hitl.ContractVersion,
		"item_id":          r.itemID(st.Op, st.Item),
		"caller":           map[string]any{"application_id": caller},
	}
	if len(st.Fields) > 0 {
		var f map[string]any
		if err := json.Unmarshal(st.Fields, &f); err != nil {
			r.t.Fatalf("%s: bad fields: %v", st.Op, err)
		}
		for k, v := range f {
			cmd[k] = v
		}
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		r.t.Fatalf("%v", err)
	}
	return b
}

func (r *runner) step(i int, st Step) {
	t := r.t
	t.Helper()
	name := fmt.Sprintf("step %d (%s)", i+1, st.Op)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var out []byte
	var err error
	okDef := ""
	switch st.Op {
	case "enqueue":
		out, err = r.a.Enqueue(ctx, r.subst(st.Doc))
		okDef = "HITLItemHandleCoreV1"
	case "get":
		out, err = r.a.Get(ctx, r.command(st))
		okDef = "HITLGetRetrievalResultCoreV1"
	case "await":
		out, err = r.a.Await(ctx, r.command(st))
		okDef = "HITLRetrievalResultCoreV1"
	case "withdraw":
		out, err = r.a.Withdraw(ctx, r.command(st))
		okDef = "HITLTerminalOutcomeCoreV1"
	case "resolve":
		out, err = r.a.ParticipantResolve(ctx, r.itemID(name, st.Item), r.subst(st.Response))
		okDef = "HITLTerminalOutcomeCoreV1"
	case "expire":
		if err = r.a.ExpireDue(ctx); err != nil {
			t.Fatalf("%s: ExpireDue: %v", name, err)
		}
		return
	default:
		t.Fatalf("%s: unknown op", name)
	}

	wantErr := st.Expect.Code != "" || len(st.Expect.CodeIn) > 0
	if err != nil {
		if !wantErr {
			t.Fatalf("%s: unexpected error: %v (body %s)", name, err, out)
		}
		r.checkError(name, st, out, err)
		return
	}
	if wantErr {
		t.Fatalf("%s: succeeded, want error code %q%v: %s", name, st.Expect.Code, st.Expect.CodeIn, out)
	}
	body := r.validate(name, okDef, out)
	r.checkSuccess(name, st, body)
}

func (r *runner) validate(name, def string, doc []byte) map[string]any {
	r.t.Helper()
	v, err := schema.NewValidator(def)
	if err != nil {
		r.t.Fatalf("%v", err)
	}
	if err := v.Validate(doc); err != nil {
		r.t.Fatalf("%s: response does not satisfy %s: %v\n%s", name, def, err, doc)
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		r.t.Fatalf("%s: response is not an object: %v", name, err)
	}
	return m
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func obj(m map[string]any, k string) map[string]any { o, _ := m[k].(map[string]any); return o }

func num(m map[string]any, k string) int64 { f, _ := m[k].(float64); return int64(f) }

// identity pulls the item id, state and revision out of a handle, a retrieval
// result or an outcome.
func identity(body map[string]any) (id string, state string, rev int64) {
	if item := obj(body, "item"); item != nil {
		return str(item, "item_id"), str(item, "state"), num(item, "revision")
	}
	return str(body, "item_id"), str(body, "state"), firstNonZero(num(body, "revision"), num(body, "interaction_revision"))
}

func firstNonZero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

// outcomeOf returns the terminal outcome carried by a success or error body.
func outcomeOf(body map[string]any) map[string]any {
	if o := obj(body, "terminal_outcome"); o != nil {
		return o
	}
	if item := obj(body, "item"); item != nil {
		return obj(item, "terminal_outcome")
	}
	if hitl.State(str(body, "state")).IsTerminal() {
		return body
	}
	return nil
}

func (r *runner) observe(name, itemID, state string) {
	if itemID == "" || state == "" {
		return
	}
	cur := hitl.State(state)
	if prev, ok := r.observed[itemID]; ok && prev != cur && !reachable(prev, cur) {
		r.t.Errorf("%s: item %s went %s -> %s, which the lifecycle does not allow", name, itemID, prev, cur)
	}
	r.observed[itemID] = cur
}

func reachable(from, to hitl.State) bool {
	seen := map[hitl.State]bool{from: true}
	queue := []hitl.State{from}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, n := range hitl.AllStates() {
			if hitl.CanTransition(s, n) && !seen[n] {
				if n == to {
					return true
				}
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return false
}

func (r *runner) checkError(name string, st Step, out []byte, callErr error) {
	t := r.t
	t.Helper()
	if len(out) == 0 {
		t.Fatalf("%s: error %v carried no wire error body", name, callErr)
	}
	typed := hitl.DecodeError(out)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%s: error body is not JSON: %v", name, err)
	}
	code := str(m, "code")
	def := "HITLPlainErrorV1"
	switch code {
	case "stale_revision", "idempotency_conflict", "terminal_conflict":
		def = "HITLErrorV1"
	}
	r.validate(name, def, out)
	want := st.Expect.CodeIn
	if st.Expect.Code != "" {
		want = []string{st.Expect.Code}
	}
	if !slices.Contains(want, code) {
		t.Fatalf("%s: error code %q, want one of %v (%v)", name, code, want, typed)
	}
	// The typed error must round-trip to the same code family.
	switch code {
	case "stale_revision":
		if !errors.Is(typed, hitl.ErrStaleRevision) {
			t.Errorf("%s: DecodeError(%s) = %v, want ErrStaleRevision", name, code, typed)
		}
	case "idempotency_conflict":
		if !errors.Is(typed, hitl.ErrIdempotencyConflict) {
			t.Errorf("%s: DecodeError = %v, want ErrIdempotencyConflict", name, typed)
		}
	case "terminal_conflict":
		if !errors.Is(typed, hitl.ErrTerminalConflict) {
			t.Errorf("%s: DecodeError = %v, want ErrTerminalConflict", name, typed)
		}
	}
	e := st.Expect
	if code == "stale_revision" {
		r.observe(name, str(m, "item_id"), str(m, "current_state"))
	}
	if o := outcomeOf(m); o != nil {
		r.observe(name, str(o, "item_id"), str(o, "state"))
		if e.OutcomeState != "" && str(o, "state") != e.OutcomeState {
			t.Errorf("%s: outcome state %q, want %q", name, str(o, "state"), e.OutcomeState)
		}
		if e.OutcomeCause != "" && str(o, "cause") != e.OutcomeCause {
			t.Errorf("%s: outcome cause %q, want %q", name, str(o, "cause"), e.OutcomeCause)
		}
		if e.OutcomeEquals != "" {
			r.equalOutcome(name, e.OutcomeEquals, o)
		}
	} else if e.OutcomeState != "" || e.OutcomeEquals != "" {
		t.Errorf("%s: error carries no terminal_outcome, but the scenario expects one", name)
	}
	if e.ExistingItemIDIs != "" && str(m, "existing_item_id") != r.itemID(name, e.ExistingItemIDIs) {
		t.Errorf("%s: existing_item_id %q, want the item of %q", name, str(m, "existing_item_id"), e.ExistingItemIDIs)
	}
}

func (r *runner) equalOutcome(name, ref string, got map[string]any) {
	c, ok := r.vars[ref]
	if !ok {
		r.t.Fatalf("%s: unknown variable %q", name, ref)
	}
	want := outcomeOf(c.body)
	if !reflect.DeepEqual(want, got) {
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		r.t.Errorf("%s: terminal outcome changed\n was %s\n now %s", name, bytes.TrimSpace(wb), bytes.TrimSpace(gb))
	}
}

func (r *runner) checkSuccess(name string, st Step, body map[string]any) {
	t := r.t
	t.Helper()
	e := st.Expect
	id, state, rev := identity(body)
	r.observe(name, id, state)
	if e.State != "" && state != e.State {
		t.Errorf("%s: state %q, want %q", name, state, e.State)
	}
	if len(e.StateIn) > 0 && !slices.Contains(e.StateIn, state) {
		t.Errorf("%s: state %q, want one of %v", name, state, e.StateIn)
	}
	if e.Mode != "" && str(body, "mode") != e.Mode {
		t.Errorf("%s: mode %q, want %q", name, str(body, "mode"), e.Mode)
	}
	if e.WaitStatus != "" && str(body, "wait_status") != e.WaitStatus {
		t.Errorf("%s: wait_status %q, want %q", name, str(body, "wait_status"), e.WaitStatus)
	}
	if o := outcomeOf(body); o != nil {
		r.observe(name, str(o, "item_id"), str(o, "state"))
		if e.OutcomeState != "" && str(o, "state") != e.OutcomeState {
			t.Errorf("%s: outcome state %q, want %q", name, str(o, "state"), e.OutcomeState)
		}
		if e.OutcomeCause != "" && str(o, "cause") != e.OutcomeCause {
			t.Errorf("%s: outcome cause %q, want %q", name, str(o, "cause"), e.OutcomeCause)
		}
		if e.Decision != "" {
			if got := str(obj(obj(o, "resolution"), "response"), "decision"); got != e.Decision {
				t.Errorf("%s: decision %q, want %q", name, got, e.Decision)
			}
		}
		if e.OutcomeEquals != "" {
			r.equalOutcome(name, e.OutcomeEquals, o)
		}
	} else if e.OutcomeState != "" || e.OutcomeEquals != "" || e.Decision != "" {
		t.Errorf("%s: result carries no terminal outcome, but the scenario expects one", name)
	}
	if e.SameItemAs != "" && id != r.itemID(name, e.SameItemAs) {
		t.Errorf("%s: item %q, want the item of %q (%q)", name, id, e.SameItemAs, r.itemID(name, e.SameItemAs))
	}
	if e.DistinctItemFrom != "" && id == r.itemID(name, e.DistinctItemFrom) {
		t.Errorf("%s: item %q must differ from the item of %q", name, id, e.DistinctItemFrom)
	}
	if e.RevisionSameAs != "" && rev != r.vars[e.RevisionSameAs].revision {
		t.Errorf("%s: revision %d, want the revision of %q (%d)", name, rev, e.RevisionSameAs, r.vars[e.RevisionSameAs].revision)
	}
	if e.RevisionGreaterThn != "" && rev <= r.vars[e.RevisionGreaterThn].revision {
		t.Errorf("%s: revision %d, want more than the revision of %q (%d)", name, rev, e.RevisionGreaterThn, r.vars[e.RevisionGreaterThn].revision)
	}
	if st.As != "" {
		r.vars[st.As] = &capture{itemID: id, revision: rev, body: body}
	}
}
