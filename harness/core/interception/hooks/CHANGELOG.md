# Changelog

## Retirement — 2026-10-09

- Redirect new development to `github.com/hollis-labs/plugin-hooks` in `github.com/hollis-labs/plugin-hooks v0.1.0`.
- The successor remains standalone by design. Its catalog engine is a clean break from the historical eleven-event contract; adapt hook registration, dispatch and policy rather than rewriting an import blindly.
- Archive after the final redirect merge; retain all historical source and tags.

All notable changes to go-hooks are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-29

### Added

- Root package `hooks`: `Event` (eleven core events) with `Valid`, `Events`
  and `UsesToolMatcher`; `CommonInput` and one `*Input` type per event;
  `Decision` (`allow`, `deny`, `ask`); `Output` with `Validate`; `Kind`,
  `OnError` (no default), `Layer`, `MCPToolRef`; `Hook` with `Validate`
  reporting every violated field as `*ValidationError`; `Resolve` (pure,
  whole-hook precedence by name, managed > user > project); `MatchesTool`
  (Claude Code's matcher rule: match-all, exact name or `|`/`,` list, else an
  unanchored regex; not a glob); `TruncateContext`.
- Known unverified against the live hooks reference; verify before relying on
  this for a real Claude Code integration: the `PostToolUse` result field name
  (`tool_result`) and whether a PostToolUse output can rewrite the result
  (`updatedToolOutput`); `PermissionRequest`'s output shape (Claude nests it as
  `hookSpecificOutput.decision.behavior`/`updatedInput`, unlike this flat
  `Output`) and `PermissionRequestInput.Reason`; `SubagentStop`'s output
  (`decision`/`reason` at top level plus `hookSpecificOutput.additionalContext`
  rather than `continue`/`stopReason`). Regex matchers are Go RE2, not
  JavaScript.
- `cmdhook.Runner.Run`: runs a command-kind hook with JSON on stdin, exit 0
  decodes stdout, exit 2 is a deny block, other nonzero exits and timeouts
  are errors; the timeout is enforced without a caller deadline.
- `conformance`: embedded fixture tree with real executable scripts covering
  all eleven events and the allow, deny, block and failure paths for
  PreToolUse and PermissionRequest, plus `Load` and `Run`.
