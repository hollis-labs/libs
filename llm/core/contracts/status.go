package agentcontracts

import (
	"errors"
	"fmt"
)

// InstanceStatus is the lifecycle state of a running agent instance. The set is
// fixed at seven values.
type InstanceStatus string

// The instance statuses.
const (
	StatusStarting InstanceStatus = "starting"
	StatusRunning  InstanceStatus = "running"
	StatusWaiting  InstanceStatus = "waiting"
	StatusPaused   InstanceStatus = "paused"
	StatusStopping InstanceStatus = "stopping"
	StatusStopped  InstanceStatus = "stopped"
	StatusRejected InstanceStatus = "rejected"
)

// Valid reports whether s is one of the seven statuses.
func (s InstanceStatus) Valid() bool {
	switch s {
	case StatusStarting, StatusRunning, StatusWaiting, StatusPaused,
		StatusStopping, StatusStopped, StatusRejected:
		return true
	default:
		return false
	}
}

// WaitingReason says what a waiting instance is waiting for.
type WaitingReason string

// The waiting reasons.
const (
	WaitingInput    WaitingReason = "input"
	WaitingAuth     WaitingReason = "auth"
	WaitingApproval WaitingReason = "approval"
)

// Valid reports whether w is a defined waiting reason.
func (w WaitingReason) Valid() bool {
	switch w {
	case WaitingInput, WaitingAuth, WaitingApproval:
		return true
	default:
		return false
	}
}

// StoppedReason says how a stopped instance ended. "completed" replaces the
// legacy "done"; there is no alias.
type StoppedReason string

// The stopped reasons.
const (
	ReasonCompleted StoppedReason = "completed"
	ReasonFailed    StoppedReason = "failed"
	ReasonCanceled  StoppedReason = "canceled"
)

// Valid reports whether r is a defined stopped reason.
func (r StoppedReason) Valid() bool {
	switch r {
	case ReasonCompleted, ReasonFailed, ReasonCanceled:
		return true
	default:
		return false
	}
}

// StoppedCause refines a [StoppedReason]. It is a working default that may be
// deferred; deleting it and its five constants is mechanical.
type StoppedCause string

// The stopped causes, each valid under exactly one reason.
const (
	CauseRequested StoppedCause = "requested" // canceled
	CauseKilled    StoppedCause = "killed"    // canceled
	CauseError     StoppedCause = "error"     // failed
	CauseCrashed   StoppedCause = "crashed"   // failed
	CauseLost      StoppedCause = "lost"      // failed; replaces the legacy "orphaned"
)

// Valid reports whether c is a defined stopped cause.
func (c StoppedCause) Valid() bool {
	return c.reason() != ""
}

// reason is the one StoppedReason under which c is valid, or "" for an
// undefined cause.
func (c StoppedCause) reason() StoppedReason {
	switch c {
	case CauseRequested, CauseKilled:
		return ReasonCanceled
	case CauseError, CauseCrashed, CauseLost:
		return ReasonFailed
	default:
		return ""
	}
}

// StoppedDetail is why an instance is in [StatusStopped]. Cause is empty for a
// completed stop and optional for the others.
type StoppedDetail struct {
	Reason StoppedReason `json:"reason" yaml:"reason"`
	Cause  StoppedCause  `json:"cause,omitempty" yaml:"cause,omitempty"`
}

// Valid reports whether d is a defined reason with a cause that belongs to it.
func (d StoppedDetail) Valid() bool { return d.Validate() == nil }

// Validate reports why d is not a coherent stopped detail.
func (d StoppedDetail) Validate() error {
	var errs []error
	if !d.Reason.Valid() {
		errs = append(errs, fmt.Errorf("stopped.reason %q is not completed, failed or canceled", d.Reason))
	}
	if d.Cause != "" {
		switch {
		case !d.Cause.Valid():
			errs = append(errs, fmt.Errorf("stopped.cause %q is not a defined cause", d.Cause))
		case d.Reason.Valid() && d.Cause.reason() != d.Reason:
			errs = append(errs, fmt.Errorf("stopped.cause %q does not belong to reason %q", d.Cause, d.Reason))
		}
	}
	return errors.Join(errs...)
}

// A2ATaskState mirrors the A2A protocol's task lifecycle. The names come from a
// search summary, not the specification read end to end; verify them against
// a2a-protocol.org before treating them as final.
type A2ATaskState string

// The A2A task states.
const (
	A2ASubmitted     A2ATaskState = "submitted"
	A2AWorking       A2ATaskState = "working"
	A2AInputRequired A2ATaskState = "input-required"
	A2AAuthRequired  A2ATaskState = "auth-required"
	A2ACompleted     A2ATaskState = "completed"
	A2AFailed        A2ATaskState = "failed"
	A2ACanceled      A2ATaskState = "canceled"
	A2ARejected      A2ATaskState = "rejected"
)

// Valid reports whether a is one of the eight A2A states.
func (a A2ATaskState) Valid() bool {
	switch a {
	case A2ASubmitted, A2AWorking, A2AInputRequired, A2AAuthRequired,
		A2ACompleted, A2AFailed, A2ACanceled, A2ARejected:
		return true
	default:
		return false
	}
}

// ToA2A maps an instance status onto the nearest A2A task state. w is read only
// for [StatusWaiting] and d only for [StatusStopped].
//
// The mapping is lossy. A2A has no exact counterpart for starting (mapped to
// submitted), paused or stopping (both mapped to working: not terminal, not
// asking anything). waiting.approval is PROVISIONAL: no A2A state cleanly means
// "waiting on human approval", so it maps to input-required, the nearest, until
// the human-in-the-loop crosswalk settles it.
//
// An invalid status, a waiting status with an undefined reason, or a stopped
// status with an undefined reason returns the empty A2ATaskState, which is not
// [A2ATaskState.Valid].
func (s InstanceStatus) ToA2A(w WaitingReason, d StoppedDetail) A2ATaskState {
	switch s {
	case StatusStarting:
		return A2ASubmitted
	case StatusRunning, StatusPaused, StatusStopping:
		return A2AWorking
	case StatusWaiting:
		switch w {
		case WaitingInput, WaitingApproval:
			return A2AInputRequired
		case WaitingAuth:
			return A2AAuthRequired
		default:
			return ""
		}
	case StatusStopped:
		switch d.Reason {
		case ReasonCompleted:
			return A2ACompleted
		case ReasonFailed:
			return A2AFailed
		case ReasonCanceled:
			return A2ACanceled
		default:
			return ""
		}
	case StatusRejected:
		return A2ARejected
	default:
		return ""
	}
}
