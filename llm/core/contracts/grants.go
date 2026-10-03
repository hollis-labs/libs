package contracts

// MCPGrant is the ceiling for MCP server and tool visibility, not the
// per-launch profile derived from it (that is tool selection's job).
type MCPGrant struct {
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty" yaml:"deny,omitempty"`
}

// Grants is the CEILING on what a run may use. A definition's tools are a
// request under it; the effective set is the intersection.
//
// Permissions is deliberately an opaque list of glob/rule tokens: the
// permission engine interprets it and this package does not import it.
type Grants struct {
	Tools       []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	Permissions []string `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	MCP         MCPGrant `json:"mcp,omitempty" yaml:"mcp,omitempty"`
}
