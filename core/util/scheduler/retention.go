package scheduler

import (
	"sort"
	"time"
)

// Terminal reports whether a fire no longer has a dispatch/retry obligation.
func (s FireStatus) Terminal() bool {
	return s == FireSucceeded || s == FireSkipped || s == FireExhausted
}

// PrunableFires selects terminal records strictly older than olderThan while
// protecting each schedule's newest keepLastN terminal records. Ordering uses
// scheduled time then stable ID; retained active fires never count against N.
func PrunableFires(fires []Fire, keepLastN map[string]int, olderThan time.Time) []Fire {
	terminal := make([]Fire, 0, len(fires))
	for _, f := range fires {
		if f.Status.Terminal() {
			terminal = append(terminal, f)
		}
	}
	sort.Slice(terminal, func(i, j int) bool {
		a, b := terminal[i], terminal[j]
		if a.ScheduleID != b.ScheduleID {
			return a.ScheduleID < b.ScheduleID
		}
		if !a.ScheduledAt.Equal(b.ScheduledAt) {
			return a.ScheduledAt.After(b.ScheduledAt)
		}
		return a.ID > b.ID
	})
	seen := map[string]int{}
	var out []Fire
	for _, f := range terminal {
		seen[f.ScheduleID]++
		if seen[f.ScheduleID] > keepLastN[f.ScheduleID] && f.ScheduledAt.Before(olderThan) {
			out = append(out, f)
		}
	}
	return out
}
