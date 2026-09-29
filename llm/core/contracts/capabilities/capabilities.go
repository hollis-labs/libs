package capabilities

// Level distinguishes a hard need from a soft preference.
type Level string

const (
	// LevelRequires is a hard need: a launch refuses unless the caller forces it.
	LevelRequires Level = "requires"
	// LevelUses is a soft need: the agent degrades gracefully without it.
	LevelUses Level = "uses"
)

// Valid reports whether l is one of the two levels.
func (l Level) Valid() bool {
	switch l {
	case LevelRequires, LevelUses:
		return true
	default:
		return false
	}
}

// Name is a capability name. The vocabulary is open: see [Known].
type Name string

// The capability vocabulary.
const (
	Identity    Name = "identity"
	Memory      Name = "memory"
	Resume      Name = "resume"
	LongRunning Name = "long-running"
	Skills      Name = "skills"
	Subagents   Name = "subagents"
	MCP         Name = "mcp"
	Sandbox     Name = "sandbox"
	Hooks       Name = "hooks"
)

// known is the closed list behind [Known]; it is not exported so the
// vocabulary can only grow through a new constant and a new entry here.
var known = map[Name]struct{}{
	Identity: {}, Memory: {}, Resume: {}, LongRunning: {}, Skills: {},
	Subagents: {}, MCP: {}, Sandbox: {}, Hooks: {},
}

// Known reports whether n is in the vocabulary this package defines. An
// unknown name is not invalid — a host may support more — but static tooling
// cannot verify it.
func Known(n Name) bool {
	_, ok := known[n]
	return ok
}

// Declaration is one capability an agent asks for, or a host supports.
type Declaration struct {
	Name Name `json:"name" yaml:"name"`
	// Level is empty on a host's supports entry.
	Level Level `json:"level,omitempty" yaml:"level,omitempty"`
}

// Set is a list of capability names.
type Set []Name

// Has reports whether n is in s.
func (s Set) Has(n Name) bool {
	for _, have := range s {
		if have == n {
			return true
		}
	}
	return false
}

// Check reports which of required are absent from supported, in the order
// required lists them, without duplicates. It returns nil when nothing is
// missing.
//
// Check accepts no force flag. Overriding an unmet requirement is the caller's
// decision to make and to record; it is never this function's to grant.
func Check(supported, required Set) (missing Set) {
	for _, r := range required {
		if !supported.Has(r) && !missing.Has(r) {
			missing = append(missing, r)
		}
	}
	return missing
}
