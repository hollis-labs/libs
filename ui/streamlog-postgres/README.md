# streamlog-postgres

The PostgreSQL `streamhub.Log` (from `ui/ui-go`): the target store of the platform event log, with the same contract and columns as `ui/streamlog-sqlite`.

```go
l, err := streamlogpostgres.OpenDSN(ctx, "postgres://…", streamlogpostgres.Options{Schema: "streamlog"})
rec, err := l.Append(ctx, environmentID, streamhub.Event{ID: "evt_…", Name: "task.state_changed", Data: envelopeJSON})
cursor := logkit.FormatCursor(rec.Epoch, rec.Seq)
for r, err := range l.AfterCursor(ctx, environmentID, cursor) { … }
```

- Several writers may append to one stream; cursors stay gap-free and ordered.
- Restore detection combines the server's system identifier + timeline (stored in the log) with an external marker file on the writer's host. A failover or point-in-time recovery re-mints the epoch; readers get an `epoch_changed` gap and resume. See `doc.go`.
- Not partitioned yet: partitioning needs global `event_id` uniqueness to move to its own table first.
- Tests: `STREAMLOG_PG_DSN=postgres://user:pass@host:port/db go test ./...` (each test uses its own schema).
