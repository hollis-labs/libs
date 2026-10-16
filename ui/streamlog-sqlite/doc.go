// Package streamlogsqlite is a durable [streamhub.Log] on SQLite.
//
// It is the shared event log for daemons that need replayable history across
// restarts: per-stream cursors assigned by the log, an epoch that identifies
// the log, idempotent appends, head-only trimming with computed gaps, and
// indexed scope columns for the platform event envelope. The log itself is
// envelope-agnostic: [Event.Name] is the kind and [Event.Data] is opaque; an
// [Extractor] lifts the indexed columns out of the data.
//
// # Streams, cursors, epoch
//
// A stream is whatever the caller keys cursors by; the platform uses one
// stream per environment. Cursors start at 1, are gap-free and are never
// reused, including after a trim and after a restart.
//
// The epoch is the identity of the LOG. It is not a producer's restart
// counter (an envelope's generation, which the log stores and never
// interprets). It is minted when the database is created and stays the same
// across restarts. It is re-minted whenever the database is recreated or
// restored, because cursor values under the old epoch would then name
// different events. [FormatCursor] and [ParseCursor] encode "<epoch>:<seq>";
// [Log.AfterCursor] answers a stale epoch with a [streamhub.GapEpochChanged]
// gap instead of records.
//
// # Gaps
//
// Gaps are computed, not stored: trim is head-only, so a gap is fully
// determined by the stream's pruned-through cursor and the epoch. Every gap
// carries a reason, the earliest available cursor, the high-water cursor and
// the epoch; an empty gap frame is never produced.
//
// # Restore detection
//
// A small marker file next to the database (outside the database file)
// records the epoch and each stream's high-water cursor. On Open, a marker
// that disagrees with the database (a different epoch, or a stream whose head
// is behind the marker) means the database was replaced or rolled back. The
// default is to re-mint the epoch and report it in [Log.Recovery];
// [Options.OnStorageMismatch] can make Open fail instead.
//
// # Concurrency
//
// One process owns the database. Appends and trims go through a single
// writer connection; reads use a separate pool and never block on iteration.
// Several writer processes on one file are not supported.
package streamlogsqlite
