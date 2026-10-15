// Package quota counts usage against limits: windows, counters, limits and a
// Check / Reserve / Commit flow, with an injectable clock and pluggable stores.
//
// A Limit is a budget of Max units (in the provider's or application's own
// unit, never converted) for a Key over a Window. Windows come in three kinds,
// and windows with different IDs never share a counter:
//
//   - Calendar: a day, ISO week or month in a named time zone, following the
//     local wall clock across daylight-saving changes.
//   - Rolling: a trailing duration.
//   - Provider: what the provider reports (remaining and reset time), given to
//     the Limiter with Observe.
//
// A Limiter answers with a Decision{Allowed, Remaining, RetryAfter, Reason}.
// Reserve holds an estimate for a request in flight; Commit replaces it with
// the actual amount (estimate-then-reconcile, for streaming responses whose
// size is known only at the end); Cancel gives it back.
//
// Committed usage lives in a Store. The package has three:
//
//   - MemoryStore keeps usage in memory.
//   - SeededStore fills a memory copy from the application's own history (an
//     audit log, an events table) on first use and after Reseed.
//   - SQLStore reads the application's own events table through database/sql
//     on every decision.
//
// The library never opens a database, never writes the application's events
// and never deletes them: counters derive from what the application already
// records, and budget history stays wherever the application keeps it.
//
// quotatest holds a conformance suite for Store implementations.
package quota
