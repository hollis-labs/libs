package agentcontracts

// Task carries what to do. There is no instructions-append field: task
// instructions live in Input only, and the definition body is never replaced.
type Task struct {
	Input string   `json:"input" yaml:"input"`
	Files []string `json:"files,omitempty" yaml:"files,omitempty"`
}
