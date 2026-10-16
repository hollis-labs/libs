package mw

import (
	"errors"
	"fmt"
	"strings"
)

// Phase orders middleware, outermost first. Assembly rejects a chain whose
// phases are not non-decreasing.
type Phase int

const (
	// PhaseRecover turns a panic below it into an error; it is outermost.
	PhaseRecover Phase = iota + 1
	PhaseTrace         // trace and request ID
	// PhaseAccessLog records one line per call.
	PhaseAccessLog
	PhaseLimits // timeout, body size, request validation
	PhaseCORS   // HTTP only
	// PhaseAuthn establishes who is calling.
	PhaseAuthn
	PhaseAuthz // authorization and policy
	// PhaseRateLimit bounds rate and concurrency.
	PhaseRateLimit
	PhaseIdentity // identity enrichment
	// PhaseCache answers repeat calls without running the handler.
	PhaseCache
	PhaseOutcome // innermost: shapes the handler result (job retry/backoff classification)
)

var phaseNames = map[Phase]string{
	PhaseRecover: "recover", PhaseTrace: "trace", PhaseAccessLog: "access-log",
	PhaseLimits: "limits", PhaseCORS: "cors", PhaseAuthn: "authn", PhaseAuthz: "authz",
	PhaseRateLimit: "rate-limit", PhaseIdentity: "identity", PhaseCache: "cache", PhaseOutcome: "outcome",
}

// String returns the phase name.
func (p Phase) String() string {
	if n, ok := phaseNames[p]; ok {
		return n
	}
	return fmt.Sprintf("phase(%d)", int(p))
}

type class int

const (
	neutral class = iota
	protects
	observes
)

func (p Phase) class() class {
	switch p {
	case PhaseLimits, PhaseAuthn, PhaseAuthz, PhaseRateLimit:
		return protects
	case PhaseTrace, PhaseAccessLog, PhaseIdentity, PhaseCache:
		return observes
	}
	return neutral
}

// FailureMode says what happens when the middleware itself breaks (panic, or a
// Fault it reports), as opposed to a decision it makes. There is no default.
type FailureMode int

const (
	// FailureUnset is the zero value. Assembly rejects it: there is no default failure mode.
	FailureUnset FailureMode = iota
	Open                     // carry on without this middleware
	Closed                   // fail the call
)

// String returns "open", "closed" or "unset".
func (f FailureMode) String() string {
	switch f {
	case Open:
		return "open"
	case Closed:
		return "closed"
	}
	return "unset"
}

// Mode is how a member is applied. Any member can run in any mode.
type Mode int

const (
	Enforce Mode = iota // zero value: apply normally
	Shadow              // evaluate and record what would have happened; apply nothing
	Off                 // skip
)

// String returns "enforce", "shadow" or "off".
func (m Mode) String() string {
	return [...]string{"enforce", "shadow", "off"}[m]
}

// Descriptor is the static contract of one middleware.
type Descriptor struct {
	Name    string
	Phase   Phase
	Failure FailureMode
	// Reason is required when Failure runs against the phase's natural default:
	// an observing phase declaring Closed, or a protecting phase declaring Open.
	Reason string
	// Mutates marks middleware that rewrites the request. It must be opted into
	// with Options.AllowMutating.
	Mutates bool
	// Requires and Provides name context keys. A requirement must be provided by
	// a member outside (earlier in the chain than) the one requiring it.
	Requires []string
	Provides []string
}

// Options tunes assembly.
type Options struct {
	// AllowMutating lists the names of mutating members the assembler may accept.
	AllowMutating []string
	// Observer receives one Event per member evaluation. Optional.
	Observer func(Event)
}

// Validate checks a chain's descriptors, outermost first, and returns every
// problem found.
func Validate(descs []Descriptor, opts Options) error {
	var errs []error
	seen := map[string]bool{}
	provided := map[string]string{}
	allowed := map[string]bool{}
	for _, n := range opts.AllowMutating {
		allowed[n] = true
	}
	prev := Phase(0)
	for i, d := range descs {
		bad := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("%s (#%d): %s", nameOr(d.Name, i), i, fmt.Sprintf(format, a...)))
		}
		switch {
		case d.Name == "":
			bad("missing name")
		case seen[d.Name]:
			bad("duplicate name")
		}
		seen[d.Name] = true
		if _, ok := phaseNames[d.Phase]; !ok {
			bad("unknown phase %d", int(d.Phase))
		} else if d.Phase < prev {
			bad("phase %s is outside phase %s; reorder", d.Phase, prev)
		}
		if d.Phase > prev {
			prev = d.Phase
		}
		switch {
		case d.Failure != Open && d.Failure != Closed:
			bad("failure mode is required (open or closed)")
		case d.Phase.class() == observes && d.Failure == Closed && d.Reason == "":
			bad("observing phase %s declares closed without a reason", d.Phase)
		case d.Phase.class() == protects && d.Failure == Open && d.Reason == "":
			bad("protecting phase %s declares open without a reason", d.Phase)
		}
		if d.Mutates && !allowed[d.Name] {
			bad("mutates the request and is not in AllowMutating")
		}
		for _, r := range d.Requires {
			if _, ok := provided[r]; !ok {
				bad("requires %q, which no outer member provides", r)
			}
		}
		for _, p := range d.Provides {
			provided[p] = d.Name
		}
	}
	return errors.Join(errs...)
}

func nameOr(n string, i int) string {
	if n == "" {
		return fmt.Sprintf("member-%d", i)
	}
	return n
}

// Explain renders the chain: order, phases, failure modes, modes.
// modes may be nil (all enforce) or parallel to descs.
func Explain(descs []Descriptor, modes []Mode) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(fmt.Sprintf("%-3s %-16s %-11s %-7s %-8s %s", "#", "name", "phase", "failure", "mode", "notes"), " ") + "\n")
	for i, d := range descs {
		m := Enforce
		if i < len(modes) {
			m = modes[i]
		}
		var notes []string
		if d.Mutates {
			notes = append(notes, "mutates")
		}
		if len(d.Requires) > 0 {
			notes = append(notes, "needs "+strings.Join(d.Requires, ","))
		}
		if len(d.Provides) > 0 {
			notes = append(notes, "gives "+strings.Join(d.Provides, ","))
		}
		if d.Reason != "" {
			notes = append(notes, "why: "+d.Reason)
		}
		row := fmt.Sprintf("%-3d %-16s %-11s %-7s %-8s %s", i, d.Name, d.Phase, d.Failure, m, strings.Join(notes, "; "))
		b.WriteString(strings.TrimRight(row, " ") + "\n")
	}
	return b.String()
}

// Outcome of one member evaluation.
type Outcome string

const (
	Passed  Outcome = "passed"  // called next
	Denied  Outcome = "denied"  // short-circuited with an error
	Faulted Outcome = "faulted" // the middleware itself broke
)

// Event is emitted for each member evaluation, including shadow ones.
type Event struct {
	Name    string
	Phase   Phase
	Mode    Mode
	Outcome Outcome
	Err     error
}

// Fault marks an error as the middleware's own malfunction rather than a
// decision. Failure mode governs faults; plain errors are decisions.
type Fault struct {
	Err error
	// Owner is set by the guard that handled the fault. A fault with an owner
	// is already dealt with; outer members pass it through instead of treating
	// it as their own malfunction.
	Owner string
}

// Error describes the fault.
func (f *Fault) Error() string { return "middleware fault: " + f.Err.Error() }

// Unwrap returns the underlying error.
func (f *Fault) Unwrap() error { return f.Err }

// NewFault wraps err as a Fault.
func NewFault(err error) error { return &Fault{Err: err} }

// AsFault reports the Fault in err's chain, if any.
func AsFault(err error) *Fault {
	var f *Fault
	if errors.As(err, &f) {
		return f
	}
	return nil
}
