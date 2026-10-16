// Package ledgertest is the conformance suite for ledger.Store. Every
// implementation (the in-memory store, a database-backed store) must pass Run.
//
// The suite covers idempotent append, including concurrent duplicates,
// validation, one-way disposition moves, corrections as labels, outcomes, query
// filters and limits, and that returned values are copies.
package ledgertest
