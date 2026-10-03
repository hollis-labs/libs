// Package hosttest runs behavioral capability conformance through a host's
// actual admission, dispatcher, lifecycle and non-plugin bridge adapters.
//
// The harness controls fixture policy and backend barriers. Attempts are
// untrusted transport inputs; they must reach the real entry point, not call
// Enforcer directly from the test adapter. A host maps the fixture's canonical
// test resources onto its own reviewed resources and observes actual backend
// calls and audit delivery. The suite has no skipped requirements or waivers.
// Passing the SDK's reference test adapter validates the suite, not a consumer
// host, its transport profile, MCP registration surfaces or OS sandbox.
package hosttest
