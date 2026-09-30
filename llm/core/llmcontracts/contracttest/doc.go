// Package contracttest is a reusable conformance suite for
// llmcontracts.Provider implementations: any type that satisfies Provider
// can run contracttest.Run against a deterministic double to verify the
// channel-protocol invariants every implementation must honor, regardless
// of backend.
//
// contracttest does not test response content, tool-call semantics, or any
// provider-specific error taxonomy -- those remain each implementation's
// own responsibility and vary legitimately by backend.
package contracttest
