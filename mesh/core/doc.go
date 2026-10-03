// Package mesh defines portable contracts for agent providers: actor addresses,
// verbs, task and session states, live capability descriptors, negotiation,
// commands and responses, and a shared event envelope.
//
// Providers implement Provider using their own execution and messaging systems.
// Consumers negotiate exact capability versions and branch on advertised support.
// InstanceView describes an execution; it is independent of per-task TaskState.
// The root package depends only on the Go standard library.
//
// The fake subpackage implements an in-memory provider for host tests without
// launching processes. The conformance subpackage checks the MVP verbs and both
// directions of capability claims. Unsupported vocabulary verbs must return a
// typed ErrorUnsupported rather than silently falling back.
package mesh
