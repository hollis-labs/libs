# Changelog

All notable changes to agent-contracts-leaf are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.3.0 — 2026-10-01

### Added

- Package `runtimes`: the canonical runtime vocabulary (D-73). `ID` (claude, codex, opencode, copilot, pi,
  antigravity), `Mode` (streaming-stdio, subprocess-per-turn, jsonrpc-stdio, http-sse, pty, acp-stdio, acp-tcp)
  and `Capability` (resume, resume-keeps-id, typed-events, preflight, auth-classifier, session-lost-classifier,
  approvals), each closed, with `Valid` and an ordered list. The overlapping enums in go-providers, agentkit and
  go-agent-wrapper migrate to it with no aliases; the per-runtime facts live in the go-providers registry.

## v0.2.0 — 2026-09-29

### Added

- `Limits.ToolOutputBytes`, Nanite's cumulative tool-output-per-turn ceiling. `nil` means "use the
  host's default", matching the pointer-field convention every other `Limits` field already follows.

## v0.1.0 — 2026-09-29

### Added

- `Assignment`, `RunPolicy` (`lifetime`, `attach`, `attended`, `resume`), `Scope`, `Grants`, `Limits`, `Task`,
  `Launch`, `Trust` with `EffectiveTrust`, and the opaque `Requester` / `Correlation`, each with `Valid` or
  `Validate` where it has a defined set.
- `InstanceStatus` (seven values), `WaitingReason`, `StoppedReason` / `StoppedCause`, and a lossy `ToA2A`
  mapping. The A2A constants are the v0.3-era lowercase-hyphen spellings (A2A 1.0.0 uses `TASK_STATE_*` on the wire);
  `waiting.approval` maps to `input-required` provisionally.
- `LaunchRecord` with four digests, `EffectiveGrants` and `GrantDiagnostic`, so an unenforced grant is reported
  rather than silent.
- Package `capabilities`: the `Name` vocabulary, `Level`, `Declaration`, `Set`, `Known` and `Check`, which takes no
  force flag.
