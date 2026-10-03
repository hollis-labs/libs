package agentcontracts

// AgentRef names the definition to launch. Exactly one of Name or ID is
// authoritative: ID for identity:stable agents (resolved through the host's
// registry), Name (with an optional Digest) for anonymous ones. A host that does
// not support the identity capability must refuse an ID reference honestly;
// that refusal is the host's job, not Validate's.
type AgentRef struct {
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
	ID     string `json:"id,omitempty" yaml:"id,omitempty"`
	Digest string `json:"digest,omitempty" yaml:"digest,omitempty"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
}

// Isolation is a REQUEST for how isolated a run should be, not a promise that
// the host provides it.
type Isolation string

// The isolation requests.
const (
	IsolationNone     Isolation = "none"
	IsolationWorktree Isolation = "worktree"
	IsolationSandbox  Isolation = "sandbox"
)

// Valid reports whether i is one of the defined isolation requests.
func (i Isolation) Valid() bool {
	switch i {
	case IsolationNone, IsolationWorktree, IsolationSandbox:
		return true
	default:
		return false
	}
}

// ProjectRef identifies the project a run belongs to.
type ProjectRef struct {
	ID   string `json:"id,omitempty" yaml:"id,omitempty"`
	Root string `json:"root,omitempty" yaml:"root,omitempty"`
}

// Scope is where a run works.
type Scope struct {
	Project   ProjectRef `json:"project,omitempty" yaml:"project,omitempty"`
	Workdirs  []string   `json:"workdirs,omitempty" yaml:"workdirs,omitempty"`
	Isolation Isolation  `json:"isolation,omitempty" yaml:"isolation,omitempty"`
}
