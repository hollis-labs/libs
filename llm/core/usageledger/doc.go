// Package usageledger defines the shape of a durable LLM token-usage record:
// disjoint components, per-component provenance, and a derived total.
//
// A Usage has five core components (uncached input, cache read, cache write,
// output, reasoning) plus an open Dims map for provider-specific extras. Each
// Component pairs a token count with a Provenance: measured, estimated or
// unknown. A component that was not reported is unknown, never a silent zero.
// Start from NewUsage, which marks every core component unknown; a Usage built
// by bare struct literal fails Validate.
//
// The total is not a field. Usage.TotalTokens sums the components and
// Usage.TotalProvenance reports the worst provenance among them, so a stored
// total can never disagree with its parts.
//
// A Row wraps a Usage with identity and an optional PriceSnapshot, the rates
// in effect when the row was recorded. This package holds data only: it does
// no cost or pricing math, defines no storage interface, and converts nothing
// from any provider's response type. It imports only the standard library.
package usageledger
