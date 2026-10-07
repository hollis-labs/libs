// Package hosttest runs behavioral capability conformance through a host's
// actual admission, dispatcher, lifecycle and non-plugin bridge adapters.
//
// The harness controls fixture policy and backend barriers. Attempts are
// untrusted transport inputs; they must reach the real entry point, not call
// Enforcer directly from the test adapter. A host maps the fixture's canonical
// test resources onto its own reviewed resources and observes actual backend
// calls and audit delivery. The suite has no per-probe waivers. Declared unsupported descriptors are
// visible and are probed for typed refusal; empty supported profiles fail.
// ADR workflow subsystem bindings remain explicitly host-owned.
// Reference fixtures do not certify a consumer host, complete ADR coverage,
// transport profiles, MCP registration surfaces or an OS sandbox.
package hosttest
