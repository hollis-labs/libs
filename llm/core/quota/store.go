package quota

import (
	"context"
	"time"
)

// Store answers how much usage a counter has in a time range.
//
// The library never opens a database of its own. A store either keeps usage it
// is given (MemoryStore, SeededStore) or reads it from the application's own
// events (SQLStore), so a counter is derived from what the application already
// records. No store in this package deletes the application's history; any
// retention an application applies to its events must keep at least the
// longest window it limits on.
//
// Implementations must be safe for concurrent use.
type Store interface {
	// Sum returns the total amount recorded for c with from <= At < to.
	Sum(ctx context.Context, c Counter, from, to time.Time) (int64, error)
	// Entries returns the entries recorded for c with from <= At < to, oldest
	// first. The Limiter calls it only for rolling windows, to tell when usage
	// leaves the window.
	Entries(ctx context.Context, c Counter, from, to time.Time) ([]Entry, error)
}

// Recorder is implemented by stores that keep usage themselves. When the store
// is a Recorder, Reservation.Commit records the actual amount in it. When it is
// not (SQLStore), the application's own event is the record: write it before
// calling Commit, so the usage is visible before the reservation that stood in
// for it is released.
type Recorder interface {
	Record(ctx context.Context, c Counter, e Entry) error
}

// Clock tells the time. Inject one with WithClock to make window arithmetic
// deterministic in tests.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

// Now calls f.
func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
