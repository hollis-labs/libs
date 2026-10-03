// Package contracts holds the shared contract types for launching an
// agent: what to launch and under what limits ([Assignment]), how it runs
// ([RunPolicy]), and the auditable record of what was actually granted
// ([LaunchRecord]).
//
// It is pure data. There are no interfaces to satisfy, no I/O and no
// dependencies outside the standard library; the capability vocabulary lives in
// the sibling package capabilities, which this package imports and never the
// reverse. The runtime vocabulary (runtime ids, launch modes, runtime
// capabilities) lives in the sibling package runtimes.
//
// The contract is deliberately narrow:
//
//   - [Grants] are a CEILING. A definition's tools are a request under it; the
//     effective set is the intersection, computed by the host.
//   - Isolation is a request, not a promise. A grant that a host does not
//     enforce is reported as a [GrantDiagnostic] with Enforced false — never
//     silently.
//   - [EffectiveTrust] is the lower of two trust values, and nothing here can
//     raise it.
//   - [Requester] and [Correlation] are opaque couriers. This package never
//     interprets, validates or authorizes on their contents.
//   - A run policy is {lifetime, attach, attended, resume}. There is no launch
//     mode, and workspace retention is a separate policy that does not belong
//     here.
//
// What lives elsewhere: the launch-profile schema and provider/runtime
// configuration (host-side), permission decisions (go-permission), the
// definition file format (go-agentdef), and the go-materialize manifest that a
// full persisted launch record embeds (assembled by a higher layer).
package contracts
