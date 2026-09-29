# Changelog

All notable changes to go-permission are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-29

### Added

- `Engine`: allow/deny/ask checks from a mode (`default`, `accept-edits`,
  `plan`, `yolo`), ordered rules and per-session grants, plus the approval
  handshake (`RequestApproval`, `WaitForApproval`, `Respond`). Request ids use
  `crypto/rand`.
- `RuleSet` / `Rule`: YAML load, save and merge; `Resolve` for `./` patterns;
  `DeriveSubagentRuleSet` (parents' denies and asks propagate, allows do not).
- `RuleSet.Validate`, also run by `LoadRulesFromFile`: rejects an unknown
  `behavior` (a typo such as `dney` used to be silently ignored), a malformed
  tool glob and an unknown mode.
- `Auditor`, `Event`, `EventKind` and `SlogAuditor`: an audit seam covering
  decisions and every approval lifecycle step, without tool input.
- `RuleStore` and `WithRuleStore` for `ScopeProject` approvals; without a store
  a project-scope response behaves as `ScopeOnce`.
- `Matcher` and `WithMatcher` to configure the input keys and command matching.
- `WithFileEditTools` and `ToolMeta.IsFileEdit`; the default set is empty.
- `WithApprovalTimeout`, `WithAuditor`, `DefaultApprovalTimeout` (5 minutes).
- `pathgrants` sub-package: session-scoped path grants, parent-session lineage,
  `Checker`, `WithPathGrants` / `FromContext`, `ExtractPathMentions`, `HomeDir`.
- `summary` sub-package: `RenderPermissionSummary`.

### Changed (relative to the engines this was extracted from)

- `Respond` requires the exact session id of the request; an empty id no longer
  bypasses the check.
- A `/**` suffix matches on a path-segment boundary (`/work/proj/**` no longer
  matches `/work/proj-secret/x`); a `**/` prefix matches the base name or the
  full path.
- A malformed glob in a rule's `Tool` matches nothing instead of falling back to
  literal equality.
- `Evaluate` is a single pass and returns a copy of the matched rule.
- A request can be answered once; a second `Respond` no longer records a grant.
- File errors from load and save are wrapped with `%w`.
- Path values from tool input are cleaned lexically (`filepath.Clean`) before
  matching, so `/tmp/../etc/passwd` is matched as `/etc/passwd`: a `..` can no
  longer dodge a deny rule or ride an allow rule out of its tree. The stricter
  behaviour wins over the source engines, which matched the raw string.
- A `WaitForApproval` that gives up (timeout or cancelled context) claims the
  request, so a `Respond` that arrives later returns false and records nothing;
  a `Respond` that won the race is returned to the waiter instead of a deny.
  Previously a late allow could leave a session grant behind after the caller
  had been told "denied".
- `Respond` answers only with `allow` or `deny`; any other decision returns
  false and leaves the request open.

### Notes

- Check precedence is `yolo > plan > session grant > deny > ask > allow rules >
  mode default`. Session grants are keyed by tool name only. Both are pinned by
  tests and documented, not changed.

### Known limitations

- Path matching is lexical. Symlinks are not resolved, so a link is matched by
  the name it is given, not by where it points.
- `FuzzExtractPathMentions` failed once with a 10s "context deadline exceeded"
  and passed on every later run at the same budget; it is treated as a fuzz
  worker start-up timeout and has not been chased.
