package agentcontracts

// Requester says who asked for a launch. It is an opaque courier: this package
// never interprets, validates or authorizes on its contents, the same stance the
// plugin SDK takes with its opaque identity.
type Requester struct {
	ID    string            `json:"id,omitempty" yaml:"id,omitempty"`
	Kind  string            `json:"kind,omitempty" yaml:"kind,omitempty"`
	Extra map[string]string `json:"extra,omitempty" yaml:"extra,omitempty"`
}

// Correlation ties a launch to the work that caused it. Like [Requester] it is
// an opaque courier.
type Correlation struct {
	ID     string `json:"id,omitempty" yaml:"id,omitempty"`
	Parent string `json:"parent,omitempty" yaml:"parent,omitempty"`
	Trace  string `json:"trace,omitempty" yaml:"trace,omitempty"`
}
