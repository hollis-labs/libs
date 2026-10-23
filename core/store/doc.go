// Package store is the storage-driver layer for Hollis Labs apps: one set of
// handwritten SQL that runs on SQLite (tests, single box, local spools and
// caches) or on Postgres (a shared server) by switching a [Config].
//
// It is an adapter, not an ORM. An app keeps its own SQL and its own narrow
// store interfaces; go-store supplies the parts that differ between engines:
//
//   - [Open] builds a [Store] from a [Config]: a single-writer plus read-pool
//     layout on SQLite, a pgx pool on Postgres.
//   - A [Dialect] is the per-engine adapter. It rebinds "?" placeholders to
//     "$n", encodes times, spells JSON paths, and names the row-claim clause.
//   - [Store.WriteTx] and [Store.ReadTx] run work in a transaction. A panic in
//     the callback rolls the transaction back and comes out as an error that
//     carries a reference (see core/panics), never as a crash.
//   - Subpackages: migrate (goose, per-dialect directories), search (full-text
//     search on FTS5 and tsvector), outbox (transactional outbox), lint (the
//     portable-SQL rules as a check) and storetest (the conformance suite).
//
// # Portable SQL
//
// Shared SQL uses "?" placeholders, ON CONFLICT upserts, RETURNING for
// generated ids, explicit NULLS FIRST/LAST, lower() on both sides of a
// case-insensitive LIKE, Go bool for booleans and Go time.Time for every
// timestamp. See the README for the full list and the lint that enforces it.
//
// # SQLite single-writer rule
//
// SQLite has one writer. [Store.WriteTx] uses the single write connection
// inside an IMMEDIATE transaction; nothing inside the callback may use another
// connection (the [Store] itself, the read pool) or it will wait for a lock the
// transaction holds. Use only the [Tx] the callback receives.
package store
