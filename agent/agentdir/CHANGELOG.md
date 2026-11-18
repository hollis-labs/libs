# Changelog

## v0.1.0

- Add `Load` for schema-2 agent directories with stable, task, and embedded
  execution profiles. Return a content digest plus the exact MCP, permission,
  skill, and context inputs read for the snapshot.
- Add standalone MCP parsing and skill discovery, including skill metadata
  and instructions. Support literal shell environment entries on host profiles.
