// Package hookstest supplies dispatcher conformance requirements R01-R15 and
// an in-process plugin author harness. Hosts implement Factory, Dispatcher and
// Scope over their own hook path and call Run. NewEngineAdapter is the reference
// adapter; this module runs every requirement without waivers.
//
// A host that cannot meet a requirement must name it with Waive and give a
// nonempty reason. The skipped subtest prints the requirement and reason.
// Waivers document a migration gap; they do not certify full conformance.
// Requirement IDs belong to this package, independently of other test suites.
//
// These are observable semantics for the synchronous dispatcher surface.
// Async/post-commit scheduling, transport, schema compilation, authorization
// policy and application adoption require additional host integration tests.
package hookstest
