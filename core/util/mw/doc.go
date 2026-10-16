// Package mw is one middleware contract for every call surface: HTTP handlers,
// queue jobs, MCP tools and anything else that takes a request and returns a
// response.
//
// # Contract
//
// A Handler turns a request into a response; a Middleware wraps a Handler;
// Chain composes them with the first listed outermost. The contract is generic
// and standard-library only, so any module can adopt it.
//
// # Checked at assembly, not per call
//
// Every member carries a Descriptor: a name, a Phase, a FailureMode, whether it
// mutates the request, and the context keys it requires and provides. Assemble
// (and Validate) reject a chain that
//
//   - lists phases out of order (recover, trace, access-log, limits, cors, authn,
//     authz, rate-limit, identity, cache, outcome);
//   - leaves a failure mode unset (there is no default);
//   - gives a protecting phase (limits, authn, authz, rate-limit) an open
//     failure mode, or an observing phase (trace, access-log, identity, cache) a
//     closed one, without a stated Reason;
//   - includes a mutating member that was not named in Options.AllowMutating;
//   - requires a context key that no outer member provides.
//
// # Failure modes
//
// A FailureMode says what happens when the middleware itself breaks, meaning it
// panics or returns an error wrapped with NewFault, as opposed to making a
// decision such as denying a request. Open carries on without the member;
// Closed fails the call. A panic raised by the handler below a member is never
// blamed on the member.
//
// # Modes
//
// Any member can run in Enforce, Shadow or Off. Shadow runs the member against
// a stub next to learn what it would do, records an Event through
// Options.Observer, applies nothing, and calls the real handler unchanged. The
// member's own side effects (counters, log lines) still happen.
//
// # Adapters
//
// HTTP keeps its native func(http.Handler) http.Handler signature in package
// httpmw; queue jobs live in jobmw. Both share this package's Descriptor,
// Validate and Explain. Package conformance holds the scenario table every
// adapter must pass, so the same chain behaves the same on every surface.
// Further adapters (MCP tools, in the go-mcp module) run the same suite.
package mw
