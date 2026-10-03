package contracts

import (
	"errors"
	"fmt"
)

// Assignment is the shared launch contract. Orchestration that belongs to a
// task tracker — hooks, escalation, quality gates, deliverables, checkpoints,
// dependencies — stays out of it; a tracker wraps its own struct around this one.
type Assignment struct {
	Agent       AgentRef          `json:"agent" yaml:"agent"`
	Scope       Scope             `json:"scope,omitempty" yaml:"scope,omitempty"`
	Grants      Grants            `json:"grants,omitempty" yaml:"grants,omitempty"`
	Limits      Limits            `json:"limits,omitempty" yaml:"limits,omitempty"`
	Run         RunPolicy         `json:"run" yaml:"run"`
	Task        Task              `json:"task" yaml:"task"`
	Launch      Launch            `json:"launch" yaml:"launch"`
	Trust       Trust             `json:"trust,omitempty" yaml:"trust,omitempty"`
	RequestedBy Requester         `json:"requested_by,omitempty" yaml:"requested_by,omitempty"`
	Correlation Correlation       `json:"correlation,omitempty" yaml:"correlation,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Validate reports every structural problem in a: the agent reference must name
// exactly one of Name or ID, the run policy must be valid, the task must have
// input, and Scope.Isolation and Trust, when set, must be defined values.
//
// It does not check that a named agent exists, that a grant is enforceable or
// that a profile resolves; those are the host's job.
func (a Assignment) Validate() error {
	var errs []error
	switch {
	case a.Agent.Name == "" && a.Agent.ID == "":
		errs = append(errs, errors.New("agent: one of name or id is required"))
	case a.Agent.Name != "" && a.Agent.ID != "":
		errs = append(errs, errors.New("agent: exactly one of name or id, not both"))
	}
	if err := a.Run.Validate(); err != nil {
		errs = append(errs, err)
	}
	if a.Task.Input == "" {
		errs = append(errs, errors.New("task.input is required"))
	}
	if a.Scope.Isolation != "" && !a.Scope.Isolation.Valid() {
		errs = append(errs, fmt.Errorf("scope.isolation %q is not none, worktree or sandbox", a.Scope.Isolation))
	}
	if a.Trust != "" && !a.Trust.Valid() {
		errs = append(errs, fmt.Errorf("trust %q is not trusted, normal or untrusted", a.Trust))
	}
	return errors.Join(errs...)
}
