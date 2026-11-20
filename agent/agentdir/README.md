# Authored agent directories

`github.com/hollis-labs/libs/agent/agentdir` reads and validates an authored
directory for a host that owns its execution and state.

```go
snapshot, err := agentdir.Load(dir)
if err != nil {
	return err
}
// Use snapshot.Definition, snapshot.Profile, and snapshot.Digest.
```

| Path | Format |
| --- | --- |
| `agent.md` | Required schema-2 `mesh/core/agentdef` YAML frontmatter and Markdown instructions |
| `host.yaml` | Required host kind, lifetime, identity, and optional runtime and permissions |
| `.mcp.json` | Optional Claude-shaped `mcpServers` object for stdio, streamable HTTP, or SSE |
| `permissions.yaml` | Optional authored bytes for the host's policy parser |
| `skills/<name>/SKILL.md` | Optional YAML frontmatter with name and description, followed by instructions |
| `context/*.md` | Optional direct Markdown context files |

The profile supports [stable](examples/agents/stable), [task](examples/agents/task),
and [embedded](examples/agents/embedded) agents. They bind respectively to a
seat, task, or application identifier. Stable and task profiles require
durable continuity. Optional runtime settings cover provider, model, limits,
concurrency, the subtask limits (`subtask_limit`, `subtask_result_cap`), the
per-turn token budget (`turn_token_budget`), compaction (`compact_at`,
`compact_keep_tokens`, `compact_prompt`, `compact_input_tokens`), the
model stream idle timeout (`idle_timeout`), and literal shell environment
entries.

`Load` returns the definition, profile, parsed and raw MCP configuration,
raw permission rules, and the context and skill contents read for that
snapshot. Its SHA-256 digest covers the authored files by path and bytes.
Call `Load` again to observe edits. The loader expects real directories and
regular authored files up to 1 MiB each.

Use `ParseMCP` to validate standalone MCP JSON, `ListSkills` to discover
skills outside the agent directory, and `ReadSkill` to re-read an individual
skill. The caller owns secret resolution, permission rule interpretation,
model requests, and process execution.
