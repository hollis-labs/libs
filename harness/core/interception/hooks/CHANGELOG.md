# Changelog

All notable changes to go-hooks are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- Root package `hooks`: `Event` (eleven core events) with `Valid`, `Events`
  and `UsesToolMatcher`; `CommonInput` and one `*Input` type per event;
  `Decision` (`allow`, `deny`, `ask`); `Output` with `Validate`; `Kind`,
  `OnError` (no default), `Layer`, `MCPToolRef`; `Hook` with `Validate`
  reporting every violated field as `*ValidationError`; `Resolve` (pure,
  whole-hook precedence by name, managed > user > project); `MatchesTool`;
  `TruncateContext`.
- `cmdhook.Runner.Run`: runs a command-kind hook with JSON on stdin, exit 0
  decodes stdout, exit 2 is a deny block, other nonzero exits and timeouts
  are errors; the timeout is enforced without a caller deadline.
- `conformance`: embedded fixture tree with real executable scripts covering
  all eleven events and the allow, deny, block and failure paths for
  PreToolUse and PermissionRequest, plus `Load` and `Run`.
