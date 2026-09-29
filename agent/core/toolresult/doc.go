// Package toolresult keeps oversized tool results out of an LLM's context
// without losing them. A result that exceeds a byte budget is stored in full
// under a host-derived scope and replaced by a bounded preview plus a pointer;
// the agent recovers the rest with a fetch tool (byte pages, optionally
// through an RFC 6901 JSON pointer) or a search tool (RE2, line based).
//
// The package has three layers.
//
// Layer A is pure and store-free: [Preview] (JSON-aware or head-and-tail text
// preview), [BudgetForWindow] (model window to byte budget), [Select] (JSON
// pointer), [ReadPage] and [SearchPage] (UTF-8-safe paging and budgeted
// search), and [CutUTF8] (a rune-safe replacement for s[:n]).
//
// Layer B is [Cache] over a [Store]: [Cache.Present] previews and stores,
// [Cache.Put] stores without a preview, [Cache.Read] and [Cache.Search] serve
// pages back, [Cache.HandleFetch] and [Cache.HandleSearch] execute the two
// agent tools, and [Cache.Purge] deletes expired entries. Stores live in
// [github.com/hollis-labs/go-toolresult/memstore] and
// [github.com/hollis-labs/go-toolresult/sqlstore];
// [github.com/hollis-labs/go-toolresult/storetest] is the conformance suite
// for any other implementation.
//
// Layer C is [Cache.FetchSpec] and [Cache.SearchSpec]: the agent-facing tool
// definitions as plain data.
//
// # Scope
//
// Every stored entry belongs to an opaque scope string, and every read is
// scoped. The scope must be derived by the host from an authenticated session
// or caller and never taken from tool arguments, which the model controls. An
// id in another scope reads as [ErrNotFound], indistinguishable from an
// absent id.
//
// # Seam with list pagination
//
// This package handles one serialized result that is too big. Typed list
// envelopes, item counts, opaque query cursors and item-level byte or token
// fitting belong to go-mcp's budget package. The two compose in application
// code, never by import: run the list through its budget, [Cache.Put] the full
// pre-limit JSON, and append the pointer to the envelope's hint.
//
// # Failure contract
//
// [Cache.Present] returns the unmodified body together with the error when it
// cannot store, and the footer never mentions an id that was not stored. The
// host then falls back to its own truncation.
package toolresult
