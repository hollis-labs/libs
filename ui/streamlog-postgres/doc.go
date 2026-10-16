// Package streamlogpostgres is the target store for the platform event log: a
// durable [streamhub.Log] on PostgreSQL with the same contract and the same
// columns as the SQLite log (ui/streamlog-sqlite), so moving between them is a
// data copy.
//
// Cursors, the epoch, computed gaps, id idempotency and the opaque cursor
// "<epoch>:<seq>" behave exactly as in the SQLite log; see its package
// documentation. The shared pieces (cursor encoding, duplicate digest,
// extractor, marker) live in ui-go's streamhub/logkit.
//
// # Schema
//
// Everything lives in one schema (default "streamlog"). records has the
// primary key (stream, seq) and a unique index on event_id; payload is also
// kept as JSONB when the data is valid JSON. The table is not partitioned:
// partitioning needs the global event_id uniqueness to move to a separate
// unpartitioned event_ids table written in the same transaction (design
// ruling durable_log_postgres_partitions_restore_2026_10_16); that is a
// follow-up.
//
// # Writers
//
// Appends to one stream serialize on that stream's row, so cursors are
// gap-free and commit in order, from one process or from several.
//
// # Restore detection
//
// Two independent checks, both on by default, any mismatch re-mints the epoch
// (or fails Open under [RefuseOnMismatch]):
//
//   - the server's system identifier and timeline id are stored in log_meta.
//     A different system identifier means a different cluster; a different
//     timeline means a point-in-time recovery or a promotion. A failover
//     therefore also re-mints, which is safe: readers get an epoch_changed
//     gap and resume from the new epoch. Reading them needs the privilege to
//     call pg_control_system() and pg_control_checkpoint(); without it the
//     check is skipped and Open reports a warning.
//   - an external high-water marker file on the writer's host, which catches
//     what the first check cannot: a pg_restore into the same cluster or a
//     file-system or VM snapshot rollback, where neither identifier changes.
//     It can be disabled only by [Options.DisableMarker], and Open then
//     warns.
package streamlogpostgres
