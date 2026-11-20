# Changelog

## v0.2.0

- Add `runtime.subtask_limit`, `runtime.turn_token_budget` and
  `runtime.subtask_result_cap` to the host profile. `Validate` refuses
  negative values. `subtask_limit` and `subtask_result_cap` (tokens) use the
  host's default at zero; `turn_token_budget` (tokens) is unlimited at zero.
  A host that decodes
  with these fields absent is unaffected; a `host.yaml` that sets them now
  loads instead of failing on an unknown key.

## v0.1.0

- Add `Load` for schema-2 agent directories with stable, task, and embedded
  execution profiles. Return a content digest plus the exact MCP, permission,
  skill, and context inputs read for the snapshot.
- Add standalone MCP parsing and skill discovery, including skill metadata
  and instructions. Support literal shell environment entries on host profiles.
