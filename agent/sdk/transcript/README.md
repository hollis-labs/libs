# Transcript adapter (experimental v0)

`transcript` is the host-owned JSONL adapter for a single conversation. The
host chooses an authorized root, supplies the conversation ID and Claude-shaped
records, and fences stale attempt generations before opening a writer. This
package neither authenticates a caller nor chooses a conversation or provider
session. The directory name is a SHA-256 hash of the conversation ID.

`Open` holds an exclusive nonblocking file lock. `Append` compacts one JSON
object, writes one newline-terminated line with `O_APPEND`, calls `fsync`, and
returns a committed cursor. A writer crash may leave a complete but unreported
line or a torn tail. The host reconciles by replay; this package refuses a torn
tail and never truncates it automatically. A projection callback runs only after
the append is durable. A projection error returns the committed cursor with the
error, so index work can be retried without rewriting the log.

Segments are numbered from 1 with no holes. The default target is 16 MiB;
rotation happens before an append that would exceed it, except a single large
record can occupy its own segment. Records are capped at 4 MiB. Segment files
and their directory entry are synced. No retention or segment deletion policy
is supplied. A missing segment is a gap, and replay refuses a missing or
changed cursor. Cursors contain a segment number, byte offset after a complete
line, and a SHA-256 chain over every prior committed line. They are distinct
from provider event sequence, PTY offset, and attempt generation. Cursor zero
means the start of retained history.

The `MessageRecord` wrapper preserves Claude's `type`, `uuid`, `parentUuid`,
`sessionId`, `timestamp`, and `message`. The message object preserves content
blocks and usage verbatim. Assistant `tool_use` and user `tool_result` remain
linked by their native IDs. `SidebandRecord` carries approval, outcome, usage,
and compaction data as `system` or `summary` rows for host readers; ordinary
Claude ingestion can skip unfamiliar subtypes. A user tool-result message may
carry `resultRef`, a typed
`runtimecontract.Reference` to the host's immutable result receipt. Encoding
checks its shape and requires a user `tool_result` block. Reference validation
does not verify host issuance or the receipt's envelope digest; the host does
that before admission and stores the exact bytes separately.

The captured Claude Code
fixture in `testdata/claude_tool_turn.jsonl` was reduced from
`harness/core/adapters/providertest/fixtures/claude/print_tool_use.jsonl`:
the capture's scrubbed message bodies are preserved and transcript wrapper
fields are supplied. The package test vendors the ingestion expectations for
assistant/user roles, tool-use/result links, and usage. A separate test runs
the real SDK loop with its fake model and tool through a host event sink and
checks the resulting transcript. It does not claim a production host adapter
or direct compatibility with an application ingestion parser.

Public API stability is experimental v0. The host must separately provide
authorized conversation selection, attempt fencing, retention, backup,
projection retry, and full result-envelope references once the result contract
is available. The package has no independent transcript search service.
