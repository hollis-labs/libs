# streamlog-sqlite

A durable `streamhub.Log` (from `ui/ui-go`) on SQLite, for daemons that need replayable event history across restarts. One process owns the file: appends use a single writer connection, reads use their own pool.

```go
l, err := streamlogsqlite.Open(ctx, "/var/lib/app/events.db", streamlogsqlite.Options{})
rec, err := l.Append(ctx, environmentID, streamhub.Event{ID: "evt_…", Name: "task.state_changed", Data: envelopeJSON})
cursor := logkit.FormatCursor(rec.Epoch, rec.Seq) // hand this to the consumer
for r, err := range l.AfterCursor(ctx, environmentID, cursor) { … }
```

- A stream is the caller's cursor domain (one per environment). Cursors start at 1, are gap-free and never reused.
- The **epoch** identifies the log, not a producer (an envelope's `generation` is stored, never interpreted). It changes when the database is recreated or restored; a cursor from another epoch gets a `GapEpochChanged` gap.
- A marker file next to the database detects a rolled-back or replaced database at `Open`.
- Duplicate `Event.ID` with the same `Name` and `Data` (compared by sha256) returns the original record; different content is `ErrEventIDConflict`.
- Trim is head-only; gaps are computed from the pruned-through cursor, not stored.

See `doc.go` for the full contract. Postgres is the target store and will live in a sibling module behind the same interface, with the same columns.
