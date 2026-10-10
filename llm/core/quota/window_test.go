package quota_test

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata" // the DST cases must not depend on the host's zone database

	"github.com/hollis-labs/substrate/llm-core/quota"
	"github.com/hollis-labs/substrate/llm-core/quota/quotatest"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestCalendarBounds(t *testing.T) {
	chicago := mustLoad(t, "America/Chicago")
	havana := mustLoad(t, "America/Havana")
	santiago := mustLoad(t, "America/Santiago")
	utc := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, time.UTC) }

	tests := []struct {
		name         string
		w            quota.Window
		now          time.Time
		start, end   time.Time
		wantDuration time.Duration
	}{
		{"utc day", quota.Calendar(quota.Day, nil), utc(2026, 3, 4, 10, 0),
			utc(2026, 3, 4, 0, 0), utc(2026, 3, 5, 0, 0), 24 * time.Hour},
		{"spring forward day is 23h", quota.Calendar(quota.Day, chicago), utc(2026, 3, 8, 18, 0),
			utc(2026, 3, 8, 6, 0), utc(2026, 3, 9, 5, 0), 23 * time.Hour},
		{"fall back day is 25h", quota.Calendar(quota.Day, chicago), utc(2026, 11, 1, 18, 0),
			utc(2026, 11, 1, 5, 0), utc(2026, 11, 2, 6, 0), 25 * time.Hour},
		// Cuba skips 00:00 to 01:00: the day starts at 01:00 CDT.
		{"skipped midnight", quota.Calendar(quota.Day, havana), utc(2026, 3, 8, 12, 0),
			utc(2026, 3, 8, 5, 0), utc(2026, 3, 9, 4, 0), 23 * time.Hour},
		// Cuba repeats 00:00 to 01:00: the day starts at the first 00:00.
		{"repeated midnight", quota.Calendar(quota.Day, havana), utc(2026, 11, 1, 12, 0),
			utc(2026, 11, 1, 4, 0), utc(2026, 11, 2, 5, 0), 25 * time.Hour},
		{"skipped midnight, previous day ends there", quota.Calendar(quota.Day, havana), utc(2026, 3, 8, 3, 0),
			utc(2026, 3, 7, 5, 0), utc(2026, 3, 8, 5, 0), 24 * time.Hour},
		{"santiago skipped midnight", quota.Calendar(quota.Day, santiago), utc(2026, 9, 6, 12, 0),
			utc(2026, 9, 6, 4, 0), utc(2026, 9, 7, 3, 0), 23 * time.Hour},
		{"iso week starts monday", quota.Calendar(quota.Week, nil), utc(2026, 3, 8, 10, 0), // a Sunday
			utc(2026, 3, 2, 0, 0), utc(2026, 3, 9, 0, 0), 7 * 24 * time.Hour},
		{"week containing spring forward", quota.Calendar(quota.Week, chicago), utc(2026, 3, 10, 12, 0),
			utc(2026, 3, 9, 5, 0), utc(2026, 3, 16, 5, 0), 7 * 24 * time.Hour},
		{"month", quota.Calendar(quota.Month, chicago), utc(2026, 12, 31, 12, 0),
			utc(2026, 12, 1, 6, 0), utc(2027, 1, 1, 6, 0), 31 * 24 * time.Hour},
		{"month at local month end, still last month locally", quota.Calendar(quota.Month, chicago), utc(2026, 4, 1, 3, 0),
			utc(2026, 3, 1, 6, 0), utc(2026, 4, 1, 5, 0), 31*24*time.Hour - time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end, ok := tc.w.Bounds(tc.now)
			if !ok {
				t.Fatal("Bounds: not ok")
			}
			if !start.Equal(tc.start) || !end.Equal(tc.end) {
				t.Fatalf("Bounds(%s) = [%s, %s); want [%s, %s)", tc.now, start.UTC(), end.UTC(), tc.start, tc.end)
			}
			if got := end.Sub(start); got != tc.wantDuration {
				t.Fatalf("window length %s; want %s", got, tc.wantDuration)
			}
			if tc.now.Before(start) || !tc.now.Before(end) {
				t.Fatalf("now %s is outside [%s, %s)", tc.now, start, end)
			}
		})
	}
}

func TestRollingBounds(t *testing.T) {
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	start, end, ok := quota.Rolling(time.Minute).Bounds(now)
	if !ok {
		t.Fatal("not ok")
	}
	// (now-1m, now]: an entry exactly one minute old has left the window.
	if !start.Equal(now.Add(-time.Minute+time.Nanosecond)) || !end.Equal(now.Add(time.Nanosecond)) {
		t.Fatalf("Bounds = [%s, %s)", start, end)
	}
	if _, _, ok := quota.Provider("rpm").Bounds(now); ok {
		t.Fatal("provider window has local bounds")
	}
}

func TestWindowIDsAreDistinct(t *testing.T) {
	ws := []quota.Window{
		quota.Calendar(quota.Day, nil),
		quota.Calendar(quota.Day, mustLoad(t, "America/Chicago")),
		quota.Calendar(quota.Month, nil),
		quota.Rolling(24 * time.Hour),
		quota.Rolling(time.Minute),
		quota.Provider("input-tokens"),
		quota.Provider("requests"),
	}
	seen := map[string]bool{}
	for _, w := range ws {
		if err := w.Validate(); err != nil {
			t.Fatalf("%s: %v", w, err)
		}
		if seen[w.ID()] {
			t.Fatalf("duplicate window ID %q", w.ID())
		}
		seen[w.ID()] = true
	}
}

func TestWindowValidate(t *testing.T) {
	for _, w := range []quota.Window{
		{},
		quota.Rolling(0),
		quota.Rolling(-time.Second),
		quota.Provider(" "),
		quota.Calendar(quota.Period(9), nil),
	} {
		if err := w.Validate(); err == nil {
			t.Errorf("Validate(%#v) = nil; want an error", w)
		}
	}
}

// TestCalendarDSTThroughLimiter drives a Limiter with an injected clock across
// spring-forward, fall-back and a skipped local midnight.
func TestCalendarDSTThroughLimiter(t *testing.T) {
	ctx := context.Background()
	utc := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, time.UTC) }
	type usage struct {
		at     time.Time
		amount int64
	}
	tests := []struct {
		name    string
		loc     *time.Location
		now     time.Time
		history []usage
		amount  int64
		want    quota.Decision
		advance time.Duration
		after   quota.Decision
	}{
		{
			// 2026-03-08 in Chicago is 23 hours: 06:00Z to 05:00Z.
			name: "spring forward", loc: mustLoad(t, "America/Chicago"), now: utc(2026, 3, 9, 4, 30), // 23:30 CDT
			history: []usage{{utc(2026, 3, 8, 5, 59), 50}, {utc(2026, 3, 8, 6, 30), 4}}, // 23:59 CST the day before; 00:30 CST
			amount:  7, want: quota.Decision{Remaining: 6, RetryAfter: 30 * time.Minute, Reason: quota.ReasonExhausted},
			advance: 30 * time.Minute, after: quota.Decision{Allowed: true, Remaining: 10},
		},
		{
			// 2026-11-01 in Chicago is 25 hours: 05:00Z to 06:00Z.
			name: "fall back", loc: mustLoad(t, "America/Chicago"), now: utc(2026, 11, 2, 5, 0), // 23:00 CST
			history: []usage{{utc(2026, 11, 1, 5, 10), 4}}, // 00:10 CDT
			amount:  7, want: quota.Decision{Remaining: 6, RetryAfter: time.Hour, Reason: quota.ReasonExhausted},
			advance: time.Hour, after: quota.Decision{Allowed: true, Remaining: 10},
		},
		{
			// 2026-03-08 in Havana starts at 01:00 CDT (05:00Z); 00:00 does not exist.
			name: "skipped midnight", loc: mustLoad(t, "America/Havana"), now: utc(2026, 3, 8, 5, 0),
			history: []usage{{utc(2026, 3, 8, 4, 59), 50}, {utc(2026, 3, 8, 5, 0), 4}}, // 23:59 CST the day before; 01:00 CDT
			amount:  7, want: quota.Decision{Remaining: 6, RetryAfter: 23 * time.Hour, Reason: quota.ReasonExhausted},
			advance: 23 * time.Hour, after: quota.Decision{Allowed: true, Remaining: 10},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clk := quotatest.NewClock(tc.now)
			store := quota.NewMemoryStore()
			l := quota.New(store, quota.WithClock(clk))
			lim := quota.Limit{Key: "k", Unit: "u", Window: quota.Calendar(quota.Day, tc.loc), Max: 10}
			for _, u := range tc.history {
				if err := store.Record(ctx, lim.Counter(), quota.Entry{At: u.at, Amount: u.amount}); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := l.Check(ctx, lim, tc.amount); err != nil || got != tc.want {
				t.Fatalf("Check = %+v, %v; want %+v", got, err, tc.want)
			}
			clk.Advance(tc.advance)
			if got, err := l.Check(ctx, lim, tc.amount); err != nil || got != tc.after {
				t.Fatalf("after %s: Check = %+v, %v; want %+v", tc.advance, got, err, tc.after)
			}
		})
	}
}
