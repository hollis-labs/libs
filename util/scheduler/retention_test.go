package scheduler

import (
	"testing"
	"time"
)

func TestPrunableFiresProtectNewestAndEveryNonterminal(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var fires []Fire
	for i, s := range []FireStatus{FirePending, FireClaimed, FireRetrying, FireSucceeded, FireSkipped, FireExhausted} {
		fires = append(fires, Fire{ID: string(rune('a' + i)), ScheduleID: "s", ScheduledAt: at.Add(time.Duration(i) * time.Second), Status: s})
	}
	got := PrunableFires(fires, map[string]int{"s": 1}, at.Add(time.Hour))
	if len(got) != 2 || got[0].ID != "e" || got[1].ID != "d" {
		t.Fatalf("%+v", got)
	}
	if len(PrunableFires(fires, nil, at.Add(3*time.Second))) != 0 {
		t.Fatal("cutoff is strict")
	}
}
