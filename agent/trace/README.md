# trace

The per-turn trace record: one `trace.Turn` for every turn an agent runs. It
records what the turn was given (slots, tools offered), what it did (tool
calls, compactions), what it used (tokens) and how it ended. Evaluation, a
classifier's training data and debugging all read the same record.

This module holds only the schema, its validation and a span-attribute view.
The agent writes records wherever it keeps them, for example one JSON object
per line in a daily file, a host callback, and attributes on its turn span.

```go
rec := trace.Turn{Version: trace.Version, At: start, ConversationID: conv, RunID: run, TurnID: run,
	Source: trace.SourceUser, Provider: "anthropic", Model: model, /* ... */
	Outcome: trace.Outcome{Status: trace.StatusCompleted, FinishReason: "end_turn", Steps: 2}}
if err := rec.Validate(); err != nil { return err }
line, _ := json.Marshal(rec)
```

## Schema, version 1

| Field | Meaning |
| --- | --- |
| `version` | Schema version, `1`. |
| `at`, `duration_ms` | Start time and how long the turn ran. |
| `conversation_id`, `run_id`, `turn_id` | Identity. `parent_conversation_id` is set for a subagent. |
| `role`, `source` | Conversation role; what started the turn (`user`, `host`, `subagent`, `resume`, `schedule`, `reflex`, `goal`). |
| `provider`, `model` | What the turn's requests used. |
| `slots` | Assembled context slots in order: `name`, `tokens`, `changed` (the cache breaks from a changed slot on). |
| `trimmed` | Entries the selector cut or replaced with a pointer, as `slot/owner/key`. |
| `tools_offered` | Tool names offered, sorted. |
| `tool_calls` | `tool_call_id`, `tool`, `duration_ms`, `permission` (`allow`, `approved`, `deny`, `none`), `error_kind`. |
| `compactions` | `before`, `after` (estimated tokens), `forced`, `failed`, `error`. |
| `usage` | `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`; `cost` stays empty until a price is known. |
| `outcome` | `status` (`completed`, `failed`, `cancelled`), `finish_reason`, `steps`, `error` (redacted). |
| `situation`, `classified`, `why` | Reserved for the situation, classifier and selector layers. Omitted while empty. |
| `fired`, `hooks_run` | Reserved for the reflex and hook layers: `null` when the layer did not run, `[]` when it ran and produced nothing. |

Names reuse the SDK evidence recording (`run_id`, `turn_id`, `tool_call_id`,
`tool`, `model`) and Surface's context trace (`at`, `slots`, `trimmed`, `why`,
`fired`, `classified`) where they mean the same thing. Usage follows the
OpenTelemetry GenAI attribute names (`gen_ai.provider.name`,
`gen_ai.usage.cache_read.input_tokens`, ...), and `Attributes` maps a record
onto them.

The five lists every record has are always written, as `[]` when empty.
`Validate` is for writers; a reader should accept records it cannot fully
validate, since a newer one may carry a version or value it does not know.

This is not `agent/sdk/trace`, the SDK's event evidence recording. Import one
under an alias if a file needs both.

A record never carries prompts, completions, tool arguments or tool output.
Error text must be redacted before it is recorded. Filling a reserved field
does not change the version; renaming or removing a field does.
