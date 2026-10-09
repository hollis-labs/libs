package toolselect

// Tool is the minimal shape the ranker scores and returns. Name is already
// origin-qualified (for example "torque_task_create"); this package does not
// invent, resolve or validate an origin prefix.
type Tool struct {
	Server      string   // upstream/origin id
	Name        string   // origin-qualified tool name; unique within a Catalog
	Title       string   // MCP title, if the upstream declared one; "" if not
	Description string   // MCP description
	Tags        []string // server-level catalog tags (not an MCP field)
	// Arguments holds names and descriptions extracted by the caller from
	// the input schema. It contains metadata, never argument values.
	Arguments []Argument

	// ReadOnly and Destructive are MCP tool-annotation hints, passed through
	// verbatim. nil means the upstream did not declare the hint; it is never
	// converted to false.
	ReadOnly    *bool
	Destructive *bool
}

// Argument is searchable input-schema metadata. Hosts own schema parsing and
// may flatten nested property paths into Name; the ranker treats it as text.
type Argument struct {
	Name        string
	Description string
}

// Catalog is every tool one ranking call may consider, already de-duplicated
// and collision-resolved by the caller.
type Catalog struct {
	Tools []Tool
}

// MatchTier is the deterministic half of the ordering contract: a lower tier
// always sorts ahead of a higher one, whatever the scores.
type MatchTier int

// The tiers, in output order.
const (
	// TierPinned is a tool pinned by a matching [ActionOrder] rule.
	TierPinned MatchTier = iota
	// TierExactName is a tool whose Name or Title equals the query, ignoring case.
	TierExactName
	// TierPrefix is a tool whose lower-cased Name is a prefix of the
	// lower-cased query, or the reverse.
	TierPrefix
	// TierBM25 is every other returned tool, ordered by Score.
	TierBM25
)

// String returns the tier's name.
func (t MatchTier) String() string {
	switch t {
	case TierPinned:
		return "pinned"
	case TierExactName:
		return "exact_name"
	case TierPrefix:
		return "prefix"
	case TierBM25:
		return "bm25"
	default:
		return "unknown"
	}
}

// Hit is one ranked tool.
type Hit struct {
	Tool  Tool
	Tier  MatchTier
	Score float64 // BM25 score; meaningful only within TierBM25 and 0 in every other tier
}
