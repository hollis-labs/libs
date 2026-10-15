package agentdef

// SchemaVersion is the only supported file schema. There is no v1 fallback.
const SchemaVersion = "2"

// Definition is one authored YAML-frontmatter and Markdown file. Body is the
// behavior instructions, not a second serialized frontmatter field.
type Definition struct {
	SchemaVersion  string               `yaml:"schema_version" json:"schema_version"`
	DefinitionID   string               `yaml:"definition_id" json:"definition_id"`
	Revision       string               `yaml:"revision" json:"revision"`
	Name           string               `yaml:"name" json:"name"`
	Title          string               `yaml:"title,omitempty" json:"title,omitempty"`
	Description    string               `yaml:"description" json:"description"`
	Behavior       Behavior             `yaml:"behavior" json:"behavior"`
	Capabilities   []Capability         `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Requirements   Requirements         `yaml:"requirements" json:"requirements"`
	HarnessProfile HarnessProfile       `yaml:"harness_profile" json:"harness_profile"`
	Continuity     Continuity           `yaml:"continuity" json:"continuity"`
	Extensions     map[string]Extension `yaml:"extensions,omitempty" json:"extensions,omitempty"`
	Presentation   Presentation         `yaml:"presentation,omitempty" json:"presentation,omitempty"`
	Provenance     map[string]string    `yaml:"provenance,omitempty" json:"provenance,omitempty"`
	Body           string               `yaml:"-" json:"body"`
}

// Ref pins an authored dependency. URI is opaque; Digest is SHA-256 of the
// resolved content. A skill pin covers its complete packaged tree, not SKILL.md
// alone. Hosts verify those pins when resolving; this package performs no I/O.
type Ref struct {
	URI    string `yaml:"uri" json:"uri"`
	Digest string `yaml:"digest" json:"digest"`
}

type Behavior struct {
	Purpose      string   `yaml:"purpose" json:"purpose"`
	Instructions []Ref    `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	SOPs         []Ref    `yaml:"sops,omitempty" json:"sops,omitempty"`
	Hooks        []string `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Completion   string   `yaml:"completion,omitempty" json:"completion,omitempty"`
}

// Capability describes an advertised service, distinct from host support and
// executable instruction packages. IDs are opaque stable internal identifiers.
type Capability struct {
	ID          string   `yaml:"id" json:"id"`
	Description string   `yaml:"description" json:"description"`
	Domains     []string `yaml:"domains,omitempty" json:"domains,omitempty"`
	Input       *Ref     `yaml:"input,omitempty" json:"input,omitempty"`
	Output      *Ref     `yaml:"output,omitempty" json:"output,omitempty"`
}

type Requirements struct {
	Requires  []string `yaml:"requires,omitempty" json:"requires,omitempty"`
	Uses      []string `yaml:"uses,omitempty" json:"uses,omitempty"`
	Tools     []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	Skills    []Skill  `yaml:"skills,omitempty" json:"skills,omitempty"`
	Resources []Ref    `yaml:"resources,omitempty" json:"resources,omitempty"`
}

type Skill struct {
	Name    string `yaml:"name" json:"name"`
	Content Ref    `yaml:"content" json:"content"`
}

// HarnessProfile is intrinsic policy, not a host execution profile or grant.
// Content references are pinned. Permissions reference a profile name whose
// binding to a mode belongs to the harness, bounded by assignment and host policy.
type HarnessProfile struct {
	Steering    []Ref             `yaml:"steering,omitempty" json:"steering,omitempty"`
	Context     ContextPolicy     `yaml:"context" json:"context"`
	Permissions PermissionProfile `yaml:"permissions" json:"permissions"`
	Approvals   []Ref             `yaml:"approvals,omitempty" json:"approvals,omitempty"`
	Escalation  []Ref             `yaml:"escalation,omitempty" json:"escalation,omitempty"`
}

type ContextPolicy struct {
	Sources []Ref `yaml:"sources,omitempty" json:"sources,omitempty"`
	Policy  *Ref  `yaml:"policy,omitempty" json:"policy,omitempty"`
}

// PermissionProfile records a name, not a grant or a permission-mode enum.
// The semantic digest pins the name, not the host's binding-table behavior.
type PermissionProfile struct {
	Profile string `yaml:"profile" json:"profile"`
}

type ContinuityMode string

const (
	Durable   ContinuityMode = "durable"
	Ephemeral ContinuityMode = "ephemeral"
)

// Continuity is policy only. Both modes require explicit enrollment outside
// this schema; ephemeral workers do not borrow a pool's logical identity.
type Continuity struct {
	Mode             ContinuityMode `yaml:"mode" json:"mode"`
	MemoryPolicy     *Ref           `yaml:"memory_policy,omitempty" json:"memory_policy,omitempty"`
	RecoveryStrategy *Ref           `yaml:"recovery_strategy,omitempty" json:"recovery_strategy,omitempty"`
}

type Presentation struct {
	Icon   string   `yaml:"icon,omitempty" json:"icon,omitempty"`
	Avatar string   `yaml:"avatar,omitempty" json:"avatar,omitempty"`
	Tags   []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// Extension is negotiated by namespace AND version. Area associates the entire
// envelope with one of the five semantic revision areas. Unknown optional
// extensions are retained; all extension content participates in the digest.
type Extension struct {
	Version   string         `yaml:"version" json:"version"`
	Area      string         `yaml:"area" json:"area"`
	Mandatory bool           `yaml:"mandatory" json:"mandatory"`
	Data      map[string]any `yaml:"data" json:"data"`
}
