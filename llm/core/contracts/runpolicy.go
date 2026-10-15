package contracts

import (
	"errors"
	"fmt"

	"github.com/hollis-labs/substrate/llm-core/contracts/capabilities"
)

// Lifetime is how long an instance is meant to live.
type Lifetime string

// The lifetimes.
const (
	LifetimeOneShot   Lifetime = "one-shot"
	LifetimeLongLived Lifetime = "long-lived"
)

// Valid reports whether l is a defined lifetime.
func (l Lifetime) Valid() bool {
	switch l {
	case LifetimeOneShot, LifetimeLongLived:
		return true
	default:
		return false
	}
}

// ResumePolicy is when a stopped instance is resumed.
type ResumePolicy string

// The resume policies.
const (
	ResumeNever     ResumePolicy = "never"
	ResumeOnFailure ResumePolicy = "on-failure"
	ResumeAlways    ResumePolicy = "always"
)

// Valid reports whether r is a defined resume policy.
func (r ResumePolicy) Valid() bool {
	switch r {
	case ResumeNever, ResumeOnFailure, ResumeAlways:
		return true
	default:
		return false
	}
}

// RunPolicy is how a run behaves: {lifetime, attach, attended, resume}.
//
// There is no mode field and no alias for one. Workspace retention is a
// separate policy; do not add a cleanup or retention field here. Requires
// optionally states a run-level capability need beyond the definition's own
// (for example resume).
type RunPolicy struct {
	Lifetime Lifetime         `json:"lifetime" yaml:"lifetime"`
	Attach   bool             `json:"attach" yaml:"attach"`
	Attended bool             `json:"attended" yaml:"attended"`
	Resume   ResumePolicy     `json:"resume" yaml:"resume"`
	Requires capabilities.Set `json:"requires,omitempty" yaml:"requires,omitempty"`
}

// Valid reports whether Lifetime and Resume are defined values.
func (r RunPolicy) Valid() bool {
	return r.Lifetime.Valid() && r.Resume.Valid()
}

// Validate reports every way r is unusable. It checks the enums only: whether
// an unattended run may sit in a posture that prompts a human (attended=false
// with an approval-requiring grant) depends on the host's posture, so the host
// flags it; this package does not enforce it.
func (r RunPolicy) Validate() error {
	var errs []error
	if !r.Lifetime.Valid() {
		errs = append(errs, fmt.Errorf("run.lifetime %q is not one-shot or long-lived", r.Lifetime))
	}
	if !r.Resume.Valid() {
		errs = append(errs, fmt.Errorf("run.resume %q is not never, on-failure or always", r.Resume))
	}
	return errors.Join(errs...)
}
