package agentdir

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hollis-labs/libs/mesh/core/agentdef"
)

type Kind string

const (
	Stable   Kind = "stable"
	Task     Kind = "task"
	Embedded Kind = "embedded"
)

type Lifetime string

const (
	OnDemand     Lifetime = "on-demand"
	UntilTaskEnd Lifetime = "until-task-end"
	Application  Lifetime = "application"
)

// Profile is host policy, separate from agentdef's portable continuity policy.
// A task ID names one task; an application ID scopes an embedded operative.
type Profile struct {
	Kind          Kind        `yaml:"kind" json:"kind"`
	Lifetime      Lifetime    `yaml:"lifetime" json:"lifetime"`
	SeatID        string      `yaml:"seat_id,omitempty" json:"seat_id,omitempty"`
	TaskID        string      `yaml:"task_id,omitempty" json:"task_id,omitempty"`
	ApplicationID string      `yaml:"application_id,omitempty" json:"application_id,omitempty"`
	Runtime       Runtime     `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Permissions   Permissions `yaml:"permissions,omitempty" json:"permissions,omitempty"`
}

// Runtime specifies provider and turn limits. Zero values use host defaults.
type Runtime struct {
	Provider        string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model           string `yaml:"model,omitempty" json:"model,omitempty"`
	MaxOutputTokens int    `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	MaxSteps        int    `yaml:"max_steps,omitempty" json:"max_steps,omitempty"`
	ContextWindow   int    `yaml:"context_window,omitempty" json:"context_window,omitempty"`
	ToolConcurrency int    `yaml:"tool_concurrency,omitempty" json:"tool_concurrency,omitempty"`
	// SubtaskLimit is how many subtasks the host runs at once; zero uses the
	// host's default and the host may cap it.
	SubtaskLimit int `yaml:"subtask_limit,omitempty" json:"subtask_limit,omitempty"`
	// TurnTokenBudget is a token ceiling one turn shares with the subtasks it
	// starts; zero is none.
	TurnTokenBudget int `yaml:"turn_token_budget,omitempty" json:"turn_token_budget,omitempty"`
	// SubtaskResultCap is the size cap, in tokens, on the result a subtask
	// hands back; zero uses the host's default.
	SubtaskResultCap int `yaml:"subtask_result_cap,omitempty" json:"subtask_result_cap,omitempty"`
	// Env supplies literal variables to explicitly approved shell commands.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// Permissions specifies the host's permission mode.
type Permissions struct {
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

var ErrProfile = errors.New("agentdir: invalid host profile")

func (p Profile) Validate(def *agentdef.Definition) error {
	if def == nil {
		return fmt.Errorf("%w: definition required", ErrProfile)
	}
	switch p.Kind {
	case Stable:
		if p.Lifetime != OnDemand || strings.TrimSpace(p.SeatID) == "" || p.TaskID != "" || p.ApplicationID != "" || def.Continuity.Mode != agentdef.Durable {
			return fmt.Errorf("%w: stable requires durable continuity, on-demand lifetime and seat_id only", ErrProfile)
		}
	case Task:
		if p.Lifetime != UntilTaskEnd || strings.TrimSpace(p.TaskID) == "" || p.SeatID != "" || p.ApplicationID != "" || def.Continuity.Mode != agentdef.Durable {
			return fmt.Errorf("%w: task requires durable continuity, until-task-end lifetime and task_id only", ErrProfile)
		}
	case Embedded:
		if p.Lifetime != Application || strings.TrimSpace(p.ApplicationID) == "" || p.SeatID != "" || p.TaskID != "" {
			return fmt.Errorf("%w: embedded requires application lifetime and application_id only", ErrProfile)
		}
	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrProfile, p.Kind)
	}
	switch p.Runtime.Provider {
	case "", "anthropic", "openai":
	default:
		return fmt.Errorf("%w: unsupported provider %q", ErrProfile, p.Runtime.Provider)
	}
	if p.Runtime.MaxOutputTokens < 0 || p.Runtime.MaxSteps < 0 || p.Runtime.ContextWindow < 0 || p.Runtime.ToolConcurrency < 0 ||
		p.Runtime.SubtaskLimit < 0 || p.Runtime.TurnTokenBudget < 0 || p.Runtime.SubtaskResultCap < 0 {
		return fmt.Errorf("%w: runtime limits must be nonnegative", ErrProfile)
	}
	for name, value := range p.Runtime.Env {
		if !validEnvName(name) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: invalid runtime.env entry %q", ErrProfile, name)
		}
	}

	switch p.Permissions.Mode {
	case "", "default", "accept-edits", "plan", "yolo":
	default:
		return fmt.Errorf("%w: unsupported permission mode %q", ErrProfile, p.Permissions.Mode)
	}
	return nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
