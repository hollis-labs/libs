// Package launch derives a per-launch [profile.Profile] from an
// agent-contracts-leaf Assignment.
//
// [FromAssignment] intersects the Assignment's grants.mcp ceiling with a
// named base Profile. Grants are a ceiling, never an expansion: the derived
// Profile is equal to or narrower than the base, and the function never adds
// a server, an allow entry or a read-only relaxation the base did not have.
//
// # Grant entries
//
// MCPGrant.Allow and MCPGrant.Deny entries are path.Match globs on the
// origin-qualified tool name, the same as Profile.ToolsAllow and ToolsDeny. A
// grant cannot name a server on its own, because deciding which tools belong
// to a server needs a catalog and this package has none; scope a grant to a
// server with a name glob such as "github_*".
//
// # Derivation
//
// Deny entries are the union of the base's and the grant's. An empty grant
// Allow places no ceiling and leaves the base allow list alone. A non-empty
// one is intersected with the base allow list; an empty base allow list
// allows everything, so the grant Allow is used as is. Two globs are never
// intersected symbolically: an entry survives only when one side is a literal
// name that the other side matches, or when the two are identical. Anything
// else is dropped, which can only narrow. If nothing survives the result
// allows no tool at all; it is never an empty allow list, which would allow
// everything.
//
// All other Profile fields are copied unchanged. The base is never modified
// and the result shares no memory with it.
//
// This is the only package of the module that imports agent-contracts-leaf;
// the ranker and the evaluator stay standard library only.
package launch
