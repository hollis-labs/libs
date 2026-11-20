# Changelog

## Unreleased

- Add `runtime.cache_ttl` for prompt caching. Empty keeps the provider default;
  `5m` and `1h` are supported. Other values fail validation with the key named.

## v0.3.0

- Add compaction and stream keys to `runtime` in the host profile:
  `compact_at`, `compact_keep_tokens` and `compact_input_tokens` (tokens;
  zero uses the host's default), `compact_prompt` (empty uses the host's
  default) and `idle_timeout` (a Go duration such as `90s`; empty uses the
  host's default). `Validate` refuses negative values, an idle timeout
  that is not a positive duration, a NUL in the prompt, `compact_keep_tokens`
  not below `compact_at`, and `compact_at` above `context_window`; `Runtime.IdleTimeoutDuration` parses it.

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
