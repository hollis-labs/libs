package contracts

// LaunchOverrides are explicit per-launch overrides. Provider and model are
// never bare fields on the assignment.
type LaunchOverrides struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Model    string `json:"model,omitempty" yaml:"model,omitempty"`
}

// Launch selects how the host launches the agent. Profile is a reference into a
// host-side launch-profile; its schema is not this package's concern.
type Launch struct {
	Profile   string          `json:"profile" yaml:"profile"`
	Overrides LaunchOverrides `json:"overrides,omitempty" yaml:"overrides,omitempty"`
}
