package quota_test

import (
	"context"
	"fmt"
	"time"

	"github.com/hollis-labs/substrate/llm-core/quota"
	"github.com/hollis-labs/substrate/llm-core/quota/quotatest"
)

// A per-minute input-token limit: hold an estimate while the request streams,
// then commit what was really used.
func Example() {
	ctx := context.Background()
	clock := quotatest.NewClock(time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC))
	l := quota.New(quota.NewMemoryStore(), quota.WithClock(clock))
	lim := quota.Limit{Key: "model=example", Unit: "input_tokens", Window: quota.Rolling(time.Minute), Max: 1000}

	r, d, _ := l.Reserve(ctx, lim, 800)
	fmt.Println("first:", d.Allowed, d.Remaining)

	d, _ = l.Check(ctx, lim, 500)
	fmt.Println("second while the first streams:", d.Allowed, d.Reason, d.RetryAfter)

	_ = r.Commit(ctx, 450) // the response was smaller than estimated
	d, _ = l.Check(ctx, lim, 500)
	fmt.Println("second after commit:", d.Allowed, d.Remaining)

	d, _ = l.Check(ctx, lim, 5000)
	fmt.Println("oversized:", d.Allowed, d.Reason)
	// Output:
	// first: true 1000
	// second while the first streams: false exhausted 1m0s
	// second after commit: true 550
	// oversized: false request exceeds the whole limit
}

// A monthly budget in a named time zone, counted from the application's own
// history on start.
func ExampleNewSeededStore() {
	ctx := context.Background()
	chicago := time.FixedZone("CST", -6*3600)
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, chicago)
	lim := quota.Limit{Key: "team=research", Unit: "usd_micros", Window: quota.Calendar(quota.Month, chicago), Max: 50_000_000}

	// In an application this reads its audit log or events table.
	history := func(_ context.Context, since time.Time) ([]quota.SeedEntry, error) {
		return []quota.SeedEntry{
			{Counter: lim.Counter(), Entry: quota.Entry{At: time.Date(2026, 2, 28, 9, 0, 0, 0, chicago), Amount: 9_000_000}},
			{Counter: lim.Counter(), Entry: quota.Entry{At: time.Date(2026, 3, 2, 9, 0, 0, 0, chicago), Amount: 30_000_000}},
		}, nil
	}
	clock := quota.ClockFunc(func() time.Time { return now })
	l := quota.New(quota.NewSeededStore(history, 31*24*time.Hour, clock), quota.WithClock(clock))

	d, _ := l.Check(ctx, lim, 25_000_000)
	fmt.Println(d.Allowed, d.Remaining, d.Reason, d.RetryAfter)
	// Output:
	// false 20000000 exhausted 276h0m0s
}
