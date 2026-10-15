// Package agentdef describes and validates versioned reusable agent definitions.
//
// Parse reads one strict YAML-frontmatter plus Markdown file and validates it.
// ValidationError carries stable diagnostic codes, field paths and messages;
// failed parsing returns no partial definition. Schema version is "2", revision
// is an author-owned nonempty label, and definition ID is an opaque reference.
// Unknown schema keys, including nested keys, are errors. YAML aliases, anchors,
// merge keys, custom tags, duplicate keys and nonstring keys are refused.
//
// A definition contains behavior, advertised service capabilities, requirements,
// embedded harness policy and continuity. Instructions are Markdown or pinned
// behavior instruction references. Capability IDs describe services; requires
// and uses describe host support. WithCapabilities lets callers validate that
// support vocabulary. Tool requests are neither catalogue-validated nor grants.
// Continuity is durable or ephemeral; both require explicit enrollment outside
// this package, and neither mode determines execution lifetime.
//
// Harness permissions reference a nonempty opaque profile name. The harness
// owns the name-to-mode binding and enforcement. The semantic digest records
// the name, not the host binding table's behavior. Inline requests, restrictions
// and policy keys inside permissions are rejected. Team authority grants are
// separate from permission profiles.
//
// Each content reference requires a SHA-256 pin. Validation checks syntax;
// a host resolver must resolve and verify content, including the complete
// packaged tree for skills. This package performs no resource I/O, enrollment,
// process launching or permission enforcement.
//
// Extensions are keyed by lowercase reverse-DNS namespace/local name, such as
// example.org/review. Their envelopes contain version, semantic area, mandatory
// and a JSON-compatible data object. WithExtensions negotiates a validating
// handler by namespace and version. Unknown optional extensions are retained;
// unknown mandatory extensions fail validation. Claiming support without a
// handler is insufficient, and known handler failures reject optional extensions
// too. Launching consumers must apply the semantics they negotiate.
//
// Canonical emits deterministic JSON over schema version, the five semantic
// areas and all extensions, including unknown optional extensions. It normalizes
// instruction line endings and surrounding whitespace, sorts map keys and
// preserves meaningful list order. Definition ID, revision, name, title,
// presentation description, provenance and presentation do not affect semantic
// revisions; service capability descriptions do. Digest hashes this payload as
// sha256:<hex>. ArtifactDigest separately hashes exact authored file bytes.
// Canonicalization does not run extension handlers: validate with the consumer's
// options before accepting or launching a definition. For programmatic JSON
// decoding, use json.Decoder.UseNumber to preserve large extension numbers.
//
// The testdata directory contains example definitions for both continuity modes.
package agentdef
