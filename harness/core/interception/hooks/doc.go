// Package hooks is the shared hooks contract: the eleven-event vocabulary,
// the JSON shapes a hook receives and returns, the allow/deny/ask decision
// values, the per-hook failure mode, and pure layer resolution. Hosts
// implement the engine; this package is only the contract.
//
// The root package holds pure types and pure functions. It performs no I/O
// and makes no syscalls, so a non-Go adapter can read it as a schema
// reference with no execution surface attached. Running a command-kind hook
// as a subprocess lives in the cmdhook sub-package; the conformance fixtures
// and their reference runner live in the conformance sub-package. This
// package never imports either.
//
// # Events and input
//
// [Event] names the eleven core events. Each has an *Input type that embeds
// [CommonInput], so the encoded form is one flat JSON object with
// hook_event_name, session_id and cwd at the top level, matching what Claude
// Code and Codex write to a command hook's stdin.
//
// # Output and decisions
//
// [Output] is the single decoded shape for whatever a hook prints. Which
// fields an event honors is fixed by the event catalog in the README, not by
// the type. [Decision] carries "allow", "deny" and "ask" and mirrors
// go-permission's Decision values string for string without importing it.
// Claude's native wording (for example "block") is normalized to "deny" by
// the host's adapter, not here.
//
// # Registration
//
// [Hook] is one registered implementation. It requires a Timeout and an
// [OnError] mode; the zero OnError is invalid and there is no default.
// [Resolve] merges the managed, user and project layers by hook name with
// whole-hook precedence, and [Hook.Validate] must be run on the resolved
// set.
package hooks
