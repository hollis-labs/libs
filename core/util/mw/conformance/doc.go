// Package conformance runs one scenario table against every mw adapter. An
// adapter passes when the same chain of probe members yields the same call
// order, the same observer events and the same normalized outcome.
//
// An adapter supplies a Harness: it turns neutral Probes into its own member
// type, runs one request, and reports a Result. Run executes the scenarios
// (order, enforce, shadow, off, open and closed failures before and after next)
// and the assembly cases every adapter must accept or reject alike. The
// harnesses for the generic, job and HTTP adapters live here; other modules
// write their own and call Run.
package conformance
