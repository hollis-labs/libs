// Package toolselect ranks a catalog of MCP tools against a free-text query,
// deterministically, and applies the shared include/exclude/order rule schema
// while doing so.
//
// It replaces three hand-copied keyword scorers with one BM25 ranker. Build
// an [Index] once per catalog with [NewIndex] and call [Index.Rank] for each
// query, or use the one-shot [Rank].
//
// # Ordering
//
// Ranking is a total order, so the same catalog, query, rules and options
// always yield the same []Hit. Results are grouped by [MatchTier]:
// pinned tools first (from an [ActionOrder] rule), then an exact name or
// title match, then a name-prefix match, then everything else by BM25 score.
// The tier is compared before the score, never folded into it, so an exact
// name always outranks a partial match however much a description repeats the
// query words. Within the exact tier, literal names lead case-folded names,
// then titles; remaining ties break by Tool.Name ascending.
//
// Each BM25 document includes name, title, description, tags and caller-supplied
// [Argument] names/descriptions. Hosts extract schema metadata; the ranker
// neither parses schemas nor indexes argument values.
//
// A tool with no term in common with the query and no exact or prefix match
// is not returned. Pinned tools are returned regardless.
//
// # Rules
//
// [Rule] values filter and reorder the candidate set. Exclusion always wins;
// once any include rule applies, only included tools survive; [ActionOrder]
// pins tools to the front without changing membership. Equal-priority rules
// apply in declaration order.
//
// # Scope
//
// This package decides what a caller sees, not what it may call. The ReadOnly
// and Destructive fields are MCP annotation hints passed through verbatim: nil
// means the upstream did not declare the hint and is never coerced to false.
// The package is pure: no I/O, no clock, no global state, standard library
// only. The profile evaluator lives in the sibling package
// github.com/hollis-labs/substrate/agent/toolselect/profile, and the per-launch profile
// derivation from an Assignment in github.com/hollis-labs/substrate/agent/toolselect/launch,
// the only package of the module with a non-standard-library import.
package toolselect
