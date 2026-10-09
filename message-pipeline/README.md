# message-pipeline

Portable, ordered annotation stages for immutable messages. The module is
`github.com/hollis-labs/libs/message-pipeline`; package name `pipeline`.

A runner snapshots stage specifications and state, sorts ascending priority with
configuration order as the tie breaker, and calls each stage with the original
message and accumulated annotations. IDs, versions, configuration digests and
instruction digests are owner-supplied immutable identities. Callers select an
original byte ceiling. The library preserves exact UTF-8 originals and never
uses a body hash as publication identity.

The MVP permits summary annotations and `pass` only. `hold`, `drop`, fail-hold and
follow-ups are declarations reserved for a later contract; the runner rejects
activating them. Stage errors, refusal, timeout, panic and invalid output produce
closed failure traces and continue. Parent cancellation leaves work pending;
store failures and conflicting immutable input also prevent settlement. Failure
traces contain no raw errors or provider responses.

Annotations and traces are limited to 16 each, stage IDs/versions to 128 Unicode
characters, summaries to 600 characters, and their combined encoded JSON to
8 KiB. Trace durations are nonnegative integer milliseconds. Schema version 1
summary annotations map directly to adapter contracts; no application imports
are used.

Implement `ResultStore` with durable immutable `Get`/`PutIfAbsent`. Store exact
`Key` fields and `Record`, preserving the input digest and outcome. Replay reuses
successful and failed outcomes without invoking a stage. Changed content or
upstream state under the same key is a conflict, not a new summary. Changed
stage/config/instruction identities use new keys. The runner serializes its own
runs; cross-process ownership belongs to the adapter. A crash after a provider
call but before durable persistence can incur another call. Provider exactly-once
is not promised.

Implement `CursorStore` using the source's publication order. The runner never
advances cursors or delivers messages. Concrete SQLite, sink receipts, prepared
payloads and atomic cursor settlement belong to an adapter's transaction. Do not
advance a cursor across a refused sink or pending publication.

Stage timeouts include worker admission. Bounded worker slots prevent runaway
launches when a stage ignores cancellation; late answers are discarded. Go cannot
terminate such a goroutine. Stages must cooperate with cancellation and must not
perform delivery, tool calls or other effects. Provider clients belong to adapters.

Run `GOWORK=off go test -race ./...` in this directory. Releases use
`message-pipeline/vX.Y.Z` tags in the existing libs repository.
