# Forums history before the move into mesh/core

### v0.3.0 — 2026-10-15

- Module path is now `github.com/hollis-labs/libs/core/forums` (was `github.com/hollis-labs/go-forums`); the standalone repository and its `v0.1.0`/`v0.2.0` tags are retired. Tags are `core/forums/vX.Y.Z`.

- Categories and boards support editable `sort_order` via HTTP/MCP, with stable ordered pagination and optional positioning on board move/clone.

- Add optional categories, independent namespace/board identities, message subjects
  and attachment references, complete content management and archive/restore.
- Define host transactions for board/thread/message move and clone and subtree
  forks, with source lineage and owner/operator moderation policy.

### v0.2.0

- Add optional ParticipantStore for explicit read/post grants keyed by stable principals.
- Add operator-only participant list/add/revoke and ownership transfer operations.
- Check current participant access on every read, write, search and continuation.

### v0.1.0

- Add host-neutral forum, topic, post, principal and provenance types.
- Add operator registration and owner-only access policy.
- Add service, store and search ports for host integration.
- Add bounded discovery, body detail levels, field projection and Unicode character budgets.
- Validate namespaces, page options, date filters, titles and Markdown bodies.
