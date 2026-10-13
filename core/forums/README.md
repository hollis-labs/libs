# go-forums

Host-neutral forum, topic and threaded-post types, owner and explicit participant policy, bounded
read responses, and service/store/search ports. The library depends only on the
Go standard library. Tether is the first host.

Install a chosen version with `go get github.com/hollis-labs/go-forums@<version>`.
The hierarchy API described below is newer than v0.2.0; use the consuming host's
pinned revision until the next tagged release.

Released under the MIT license.

Construct `forums.New(store, searcher, newID)` and call `Service.Execute` with
an operation (`forum_create`, `forum_list`, `forum_get`, `topic_create`,
`topic_list`, `topic_get`, `post_create`, `post_list`, `post_get`, `forum_search`)
and a Request. The host constructs Caller from its authenticated context;
never deserialize it from consumer input. Tether generates topic/post UUIDv7s.
Forum and category ids are caller-chosen and unique within a host. Namespaces
are `app/<consumer>/forum/<instance>` scopes shared by multiple boards; IDs
need not match the namespace leaf.

A Store must allocate per-forum monotonic post sequences, validate reply
parents belong to the same topic, and atomically maintain posts, counts,
activity, search data and host correlation refs. List cursors must bind to
scope and filters. Searcher is independent so another host can supply its own
within-forum backend. Bodies are Markdown text; rendering belongs to clients.

Registration requires the operator; ownership defaults to that user but can
be assigned to another user or agent principal. Owner-only access includes the
host's authenticated operator. Hosts may additionally implement ParticipantStore
for operator-managed `participant_list`, `participant_add`, `participant_revoke`
and `owner_set` operations. Grants use stable authenticated principal kind/id;
`read` allows reads and discovery, `post` additionally allows topics and posts.
Service checks the current grant on every request. ListForums must filter using
the same current grants, including cursor continuation. Ownership changes do
not invite the previous owner: add a grant before transferring ownership.
Omitting both owner fields on `owner_set` selects the current operator.
Config and metadata are opaque JSON objects; `config_json.ui` is reserved for
consumer UI declarations and never interpreted by the service.

Hosts implement ManagementStore for category and content CRUD, transactional
move/clone/fork, and archive/restore. Single-level categories are optional;
boards may live directly under their namespace. The service authorizes management
for operators/board owners; participants keep read/post permissions. Hosts must
recheck structure/archive constraints atomically and maintain derived data.
Messages have optional subjects and attachment references. Clones retain original
provenance with explicit source IDs; move retains identity.

Post revisions, pins and favorites are not implemented.
Envelopes, cards, notifications and custom actions are outside MVP.

```sh
go test -race ./...
go vet ./...
```

## Host implementation and Tether transport contract

A namespace (`app/<consumer>/forum/<instance>`) groups independent boards. It
is a scope string, not a separate persisted object. IDs remain host-unique.
Categories have one level: `namespace → category → board → thread → message`.
A board may instead sit directly at the namespace root; categories cannot
contain other categories. Forum/board and topic/thread are synonymous; the
API keeps `forum`, `topic`, and `post` names.

Category registration/management requires the operator. Board/thread/message
management requires the operator or the board owner. A transfer requires that
access at both ends and stays within one namespace. Participant read/post grants
retain their existing meaning; a post grant does not grant moderation rights.
Categories are visible to their owner/operator or callers who can access a board
inside them. Categories do not grant access to their boards.

All CRUD operations are exposed through HTTP and MCP. PATCH bodies contain only
fields to change. Config/metadata replace whole JSON objects; `{}` clears them.
An empty description/subject clears it, and `attachments: []` clears attachments.
Title/body strings must be nonempty when supplied (omit unchanged values).

| Resource | Collection (POST create, GET list) | Item (GET, PATCH, DELETE) | PATCH fields |
|---|---|---|---|
| Category | `/forums/categories` | `/forums/categories/{category_id}` | `title`, `description` |
| Board | `/forums` | `/forums/{forum_id}` | `title`, `description`, `config_json`, `metadata_json` |
| Thread | `/forums/{forum_id}/topics` | `/forums/topics/{topic_id}` | `title`, `status` (`open`, `resolved`) |
| Message | `/forums/topics/{topic_id}/posts` | `/forums/posts/{post_id}` | `body_md`, `subject`, `attachments` |

Category creation takes `{id, namespace, title, description?}`. Board creation
also accepts `category_id` and `description`; omit `category_id` or use `""` for
a root board. Message creation accepts `subject` (up to 300 characters) and
`attachments` (up to 20 `{name, uri, media_type?}` references to absolute URIs).
Tether stores attachment references only; consumers own upload/download and
rendering. Creating a thread creates its metadata; create its original message
with `post_create`, then replies with `parent_post_id`.

MCP names are `tether_forum_category_{create,list,get,update,delete}`,
`tether_forum_{create,list,get,update,delete}`, and
`tether_forum_{topic,post}_{create,list,get,update,delete}`. Item operations use
`category_id`, `forum_id`, `topic_id`, or `post_id` respectively. Create uses `id`
for categories/boards and generated IDs for threads/messages.

A DELETE refuses a nonempty container/message-with-replies (409) unless its JSON
body explicitly sets `cascade: true`. Cascades remove descendants, participant
grants, search entries and deleted posts' correlation refs atomically. Empty
DELETE bodies are allowed. DELETE returns `{deleted: id}`. It is a hard delete;
use archive to retain content.

| Action | HTTP POST suffix on item route | MCP suffix / request |
|---|---|---|
| Archive/restore category, board or thread | `/archive`, `/unarchive` | `_archive`, `_unarchive` with item ID |
| Place board in category/root | `/move` | `tether_forum_move {forum_id, destination_category_id}`; `""` means root |
| Deep-copy board | `/clone` | `tether_forum_clone {forum_id, id, title?, destination_category_id?}` |
| Move thread | `/move` | `tether_forum_topic_move {topic_id, destination_forum_id}` |
| Copy thread | `/clone` | `tether_forum_topic_clone {topic_id, destination_forum_id, title?}` |
| Fork reply subtree into new thread | `/fork` | `tether_forum_topic_fork {topic_id, post_id, destination_forum_id, title?}` |
| Move/copy message and replies | `/move`, `/clone` | `tether_forum_post_move` / `_clone {post_id, destination_topic_id, parent_post_id?}` |

POST actions take JSON (`{}` for archive/unarchive). Moves preserve IDs,
authorship and provenance; cross-board moves allocate new destination sequences
and update existing session-ref URIs. Message moves include the complete reply
subtree, may reparent within the same thread, and reject reply cycles or depth
above 1000. An omitted parent places the subtree at the thread root.

Clones copy content/attachments with new IDs and explicit `source_topic_id` /
`source_post_id` lineage; original author/provenance/timestamps on messages stay
intact. They do not claim that the source session created the new copy, so new
session creation refs are not fabricated. Fork is a non-destructive subtree
copy into a new thread. Use `post_move` after creating a thread to split a
subtree out of its source. Board clones belong to the caller and do not copy
participant grants. Cloned threads clear archive state; resolved stays resolved.

Archives keep content readable. An archived category or board blocks descendant
content writes; an archived thread blocks message writes. Restoring a parent
does not clear descendants' own archive flags. Thread archive is separate from
open/resolved status, which is retained on restore. Explicit deletion and copying
out of archives are allowed, but copies cannot target an archived destination.

Board/category lists accept exact `namespace` and `archived=true|false` filters;
board lists additionally accept `category_id` (omitted: all, `""`: root only).
Thread lists accept `archived=true|false`. Omitted archive filters include both
states. These filters select each item's own archive flag, not its inherited
write restriction. Cursors bind to namespace/category/archive filters too.

Edits and content moves receive fresh per-board post sequences, and affected
thread counts/activity and search entries are updated in the transaction.
`since_seq` supports incremental post reads; it is not a deletion/change feed.
After structural changes, refresh affected lists (there are no deletion tombstones).
Mutation post responses remain compact; use `post_get detail=full` for bodies.

### Display ordering

Categories and boards expose `sort_order`, a signed JSON-safe integer
(-9007199254740991 through 9007199254740991). Lower values appear first;
IDs break ties. New records default to `0`. Set it on category/board create
or update, or on board move/clone; omission preserves the existing value
on update/move/clone, while an explicit `0` resets it. For example, use
`PATCH /forums/categories/planning` or `PATCH /forums/general` with
`{"sort_order":10}`. The corresponding MCP tools accept the same field.

Category and board list pages use `(sort_order, id)` ordering and cursors.
Filter categories by `namespace`; filter boards by `namespace` and
`category_id` for sibling ordering (`category_id=""` selects root boards).
After reordering, restart pagination to refresh the displayed collection;
these are live lists, not snapshots. Consumers can space values (10, 20, 30)
to insert items without renumbering every sibling.
