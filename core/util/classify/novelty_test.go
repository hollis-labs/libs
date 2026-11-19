package classify

import "testing"

func vec(xs ...float32) []float32 { return xs }

func TestDedupeGroupsNearCopies(t *testing.T) {
	items := []Item{
		{Text: "a", Vector: vec(1, 0, 0)},
		{Text: "a'", Vector: vec(0.99, 0.01, 0)},
		{Text: "b", Vector: vec(0, 1, 0)},
		{Text: "none"},
	}
	g := Dedupe(items, 0)
	if len(g) != 2 || len(g[0].Members) != 2 || len(g[1].Members) != 1 {
		t.Fatalf("groups %+v", g)
	}
}

func TestEmergingCountsDistinctNotCopies(t *testing.T) {
	var items []Item
	// Forty copies of one pasted log: one distinct item, never a cluster of its own.
	for i := 0; i < 40; i++ {
		items = append(items, Item{Text: "log", Vector: vec(0, 0, 1)})
	}
	// Four different phrasings of a new intent.
	items = append(items,
		// Pairwise cosines 0.76–0.94: alike, not copies.
		Item{Text: "schedule a standup", Vector: vec(1, 0.4, 0)},
		Item{Text: "book the weekly sync", Vector: vec(1, 0, 0.4)},
		Item{Text: "set up a meeting", Vector: vec(1, 0.4, 0.4)},
		Item{Text: "put a call on the calendar", Vector: vec(1, 0.1, -0.3)},
	)
	got := Emerging(items, 0.8, 3)
	if len(got) != 1 || got[0].Distinct != 4 || len(got[0].Members) != 4 {
		t.Fatalf("emerging %+v", got)
	}
	if got[0].Text == "log" {
		t.Fatal("the repeated log surfaced")
	}
	// With a minimum of one, the log appears once, carrying all forty copies.
	for _, g := range Emerging(items, 0.8, 1) {
		if g.Text == "log" && (g.Distinct != 1 || len(g.Members) != 40) {
			t.Fatalf("log group %+v", g)
		}
	}
}
