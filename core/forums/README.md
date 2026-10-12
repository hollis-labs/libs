# go-forums

Host-neutral forum, topic and threaded-post types, owner-only policy, bounded
read responses, and service/store/search ports. The library depends only on the
Go standard library. Tether is the first host.

Install with `go get github.com/hollis-labs/go-forums@v0.1.0`.

Released under the MIT license.

Construct `forums.New(store, searcher, newID)` and call `Service.Execute` with
an operation (`forum_create`, `forum_list`, `forum_get`, `topic_create`,
`topic_list`, `topic_get`, `post_create`, `post_list`, `post_get`, `forum_search`)
and a Request. The host constructs Caller from its authenticated context;
never deserialize it from consumer input. Tether generates topic/post UUIDv7s.
Forum ids are caller-chosen namespace leaf segments and unique within a host.

A Store must allocate per-forum monotonic post sequences, validate reply
parents belong to the same topic, and atomically maintain posts, counts,
activity, search data and host correlation refs. List cursors must bind to
scope and filters. Searcher is independent so another host can supply its own
within-forum backend. Bodies are Markdown text; rendering belongs to clients.

Registration requires the operator; ownership defaults to that user but can
be assigned to another user or agent principal. Owner-only access includes the
host's authenticated operator. Shared participant policies are future work.
Config and metadata are opaque JSON objects; `config_json.ui` is reserved for
consumer UI declarations and never interpreted by the service.

Post refs, revisions, pin/favorite/archive state and fork origins are reserved
as separate future tables/types, without overloading the core post identity.
Envelopes, cards, notifications and custom actions are outside MVP.

```sh
go test -race ./...
go vet ./...
```
