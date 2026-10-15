// Package hitltest is the conformance kit for go-hitl. It provides three
// things.
//
// Fixtures and Scenarios are the embedded, data-only half: one JSON document
// per fixture (valid and invalid, each invalid one recording the JSON Pointer
// it must be rejected at) and operation-sequence scenarios with expected
// results.
//
// RunConformance drives an Adapter (a wire-level view of any implementation:
// bytes in, bytes out) through every scenario, validating every response
// against the schema bundle and checking that observed state changes are
// legal under hitl.CanTransition.
//
// RunStoreContract checks that a hitl.Store keeps the atomicity Service
// relies on, and NewServiceAdapter adapts a hitl.Service to Adapter so the
// reference implementation can be run through the same suite.
package hitltest
