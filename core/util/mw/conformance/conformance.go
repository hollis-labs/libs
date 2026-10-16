package conformance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/util/mw"
)

// Behavior is what a probe member does.
type Behavior int

const (
	// Pass calls next.
	Pass        Behavior = iota
	DenyIfAsked          // short-circuit when the trigger asks for denial
	PanicBefore          // middleware itself breaks before calling next
	PanicAfter           // middleware itself breaks after next returned
)

// Probe is an adapter-neutral member: a descriptor, a mode and a behavior.
type Probe struct {
	Desc mw.Descriptor
	Mode mw.Mode
	Does Behavior
}

// Result is the adapter-neutral outcome of one request.
type Result struct {
	HandlerCalls int
	Failed       bool     // generic: error returned; HTTP: status >= 400
	Trace        []string // "name>" on the way in, "<name" on the way out
	Events       []string // "name:mode:outcome"
}

// Harness builds a chain of probes on one adapter and runs one request.
type Harness interface {
	Name() string
	// Run assembles probes (outermost first) behind a counting handler and
	// sends one request. deny asks DenyIfAsked probes to deny. A Recover member
	// is added outermost by the harness. Assembly errors are returned.
	Run(probes []Probe, deny bool) (Result, error)
	// CommittedResponse is true when the adapter cannot change the outcome after
	// the handler ran (HTTP has already written a response).
	CommittedResponse() bool
}

func event(e mw.Event) string { return fmt.Sprintf("%s:%s:%s", e.Name, e.Mode, e.Outcome) }

// Collect records events, leaving out the harness's own recover member.
func Collect(dst *[]string) func(mw.Event) {
	return func(e mw.Event) {
		if e.Name != "recover" {
			*dst = append(*dst, event(e))
		}
	}
}

func probe(name string, phase mw.Phase, fm mw.FailureMode, does Behavior) Probe {
	return Probe{Desc: mw.Descriptor{Name: name, Phase: phase, Failure: fm, Reason: "conformance"}, Does: does}
}

type scenario struct {
	name   string
	probes []Probe
	deny   bool
	// skipCommitted skips adapters whose response is committed once the handler ran.
	skipCommitted bool
	check         func(r Result) string
}

func scenarios() []scenario {
	shadow := func(p Probe) Probe { p.Mode = mw.Shadow; return p }
	off := func(p Probe) Probe { p.Mode = mw.Off; return p }
	return []scenario{
		{
			name:   "first listed is outermost",
			probes: []Probe{probe("a", mw.PhaseTrace, mw.Open, Pass), probe("b", mw.PhaseAccessLog, mw.Open, Pass)},
			check: func(r Result) string {
				return want(strings.Join(r.Trace, " "), "a> b> <b <a", r.HandlerCalls == 1 && !r.Failed)
			},
		},
		{
			name:   "enforce denial short-circuits",
			probes: []Probe{probe("auth", mw.PhaseAuthn, mw.Closed, DenyIfAsked)},
			deny:   true,
			check: func(r Result) string {
				return want(strings.Join(r.Events, ","), "auth:enforce:denied", r.HandlerCalls == 0 && r.Failed)
			},
		},
		{
			name:   "shadow denial applies nothing and records",
			probes: []Probe{shadow(probe("auth", mw.PhaseAuthn, mw.Closed, DenyIfAsked))},
			deny:   true,
			check: func(r Result) string {
				return want(strings.Join(r.Events, ","), "auth:shadow:denied", r.HandlerCalls == 1 && !r.Failed)
			},
		},
		{
			name:   "off skips",
			probes: []Probe{off(probe("auth", mw.PhaseAuthn, mw.Closed, DenyIfAsked))},
			deny:   true,
			check: func(r Result) string {
				return want(strings.Join(r.Events, ","), "", r.HandlerCalls == 1 && !r.Failed)
			},
		},
		{
			name:   "open member breaking before next: request continues",
			probes: []Probe{probe("cache", mw.PhaseCache, mw.Open, PanicBefore)},
			check: func(r Result) string {
				return want(strings.Join(r.Events, ","), "cache:enforce:faulted", r.HandlerCalls == 1 && !r.Failed)
			},
		},
		{
			name:   "closed member breaking before next: request fails, handler not run",
			probes: []Probe{probe("authz", mw.PhaseAuthz, mw.Closed, PanicBefore)},
			check: func(r Result) string {
				return want(strings.Join(r.Events, ","), "authz:enforce:faulted", r.HandlerCalls == 0 && r.Failed)
			},
		},
		{
			name:   "open member breaking after next: handler runs once, result stands",
			probes: []Probe{probe("cache", mw.PhaseCache, mw.Open, PanicAfter)},
			check: func(r Result) string {
				return want(strings.Join(r.Events, ","), "cache:enforce:faulted", r.HandlerCalls == 1 && !r.Failed)
			},
		},
		{
			name:          "closed member breaking after next: request fails",
			probes:        []Probe{probe("authz", mw.PhaseAuthz, mw.Closed, PanicAfter)},
			skipCommitted: true,
			check: func(r Result) string {
				return want(strings.Join(r.Events, ","), "authz:enforce:faulted", r.HandlerCalls == 1 && r.Failed)
			},
		},
		{
			name:   "shadow member that breaks never fails the request",
			probes: []Probe{shadow(probe("authz", mw.PhaseAuthz, mw.Closed, PanicBefore))},
			check: func(r Result) string {
				return want(strings.Join(r.Events, ","), "authz:shadow:faulted", r.HandlerCalls == 1 && !r.Failed)
			},
		},
	}
}

func want(got, exp string, ok bool) string {
	if got != exp {
		return fmt.Sprintf("got %q, want %q", got, exp)
	}
	if !ok {
		return fmt.Sprintf("outcome flags wrong (events %q)", got)
	}
	return ""
}

// invalid chains every adapter must reject, and valid ones it must accept.
func chains() (bad map[string][]Probe, good map[string][]Probe) {
	bad = map[string][]Probe{
		"phase order":      {probe("a", mw.PhaseAuthn, mw.Closed, Pass), probe("b", mw.PhaseTrace, mw.Open, Pass)},
		"duplicate name":   {probe("a", mw.PhaseTrace, mw.Open, Pass), probe("a", mw.PhaseAccessLog, mw.Open, Pass)},
		"unset failure":    {{Desc: mw.Descriptor{Name: "a", Phase: mw.PhaseTrace}}},
		"observer closed":  {{Desc: mw.Descriptor{Name: "a", Phase: mw.PhaseAccessLog, Failure: mw.Closed}}},
		"protector open":   {{Desc: mw.Descriptor{Name: "a", Phase: mw.PhaseAuthz, Failure: mw.Open}}},
		"requires missing": {{Desc: mw.Descriptor{Name: "a", Phase: mw.PhaseAuthz, Failure: mw.Closed, Requires: []string{"principal"}}}},
	}
	good = map[string][]Probe{
		"requires provided": {
			{Desc: mw.Descriptor{Name: "authn", Phase: mw.PhaseAuthn, Failure: mw.Closed, Provides: []string{"principal"}}},
			{Desc: mw.Descriptor{Name: "authz", Phase: mw.PhaseAuthz, Failure: mw.Closed, Requires: []string{"principal"}}},
		},
		"documented exceptions": {
			{Desc: mw.Descriptor{Name: "audit", Phase: mw.PhaseAccessLog, Failure: mw.Closed, Reason: "audit is the record"}},
			{Desc: mw.Descriptor{Name: "gate", Phase: mw.PhaseAuthz, Failure: mw.Open, Reason: "advisory"}},
		},
	}
	return
}

// Run executes every scenario and validation case against h.
func Run(t *testing.T, h Harness) {
	t.Helper()
	for _, sc := range scenarios() {
		t.Run("behavior/"+sc.name, func(t *testing.T) {
			if sc.skipCommitted && h.CommittedResponse() {
				t.Skip("response committed before the failure; outcome cannot change")
			}
			r, err := h.Run(sc.probes, sc.deny)
			if err != nil {
				t.Fatal(err)
			}
			if msg := sc.check(r); msg != "" {
				t.Errorf("%s: %s (result %+v)", h.Name(), msg, r)
			}
		})
	}
	bad, good := chains()
	for name, ps := range bad {
		t.Run("reject/"+name, func(t *testing.T) {
			if _, err := h.Run(ps, false); err == nil {
				t.Errorf("%s accepted an invalid chain", h.Name())
			}
		})
	}
	for name, ps := range good {
		t.Run("accept/"+name, func(t *testing.T) {
			if _, err := h.Run(ps, false); err != nil {
				t.Errorf("%s rejected a valid chain: %v", h.Name(), err)
			}
		})
	}
}
