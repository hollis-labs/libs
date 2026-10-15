package contextwindow

import (
	"reflect"
	"testing"
)

// TestSlotOrder_MatchesSeedSnapshot pins SlotOrder to a hardcoded copy of the
// sequence lifted from Nanite's internal/context/slot.go. The order is cache
// load-bearing: reordering, inserting or omitting a slot changes the
// cacheable prefix every consumer builds and silently breaks prompt-cache
// economics.
//
// This repo has no dispatcher, so it cannot replicate the cross-flavor check
// of INV1; Nanite's internal/service/slot_invariants_test.go remains the real
// multi-flavor enforcement point. This test's only job is to catch an
// accidental reorder or insertion before this repo ships a tag. Changing the
// literal below is a deliberate, reviewed act, never a fixup to make the test
// pass.
func TestSlotOrder_MatchesSeedSnapshot(t *testing.T) {
	want := []string{
		"universal",
		"system",
		"memory",
		"agent",
		"mode",
		"rules",
		"permissions",
		"workspace",
		"skills",
		"tools",
		"session",
		"context",
		"user_context",
		"handoff",
		"conversation",
	}
	if !reflect.DeepEqual(SlotOrder, want) {
		t.Fatalf("SlotOrder changed (cache-load-bearing):\n got: %q\nwant: %q", SlotOrder, want)
	}
}

// TestDefaults_MatchSeedSnapshot pins the default per-slot budgets and
// compactability, the constants derived from them, and the compaction stage
// names and order, to the values lifted from Nanite's seed.
func TestDefaults_MatchSeedSnapshot(t *testing.T) {
	wantBudgets := map[string]int{
		"universal": 550, "system": 2000, "memory": 2000, "agent": 1000,
		"mode": 500, "rules": 500, "permissions": 800, "workspace": 4000,
		"skills": 2000, "tools": 0, "session": 1000, "context": 0,
		"user_context": 2000, "handoff": 1500, "conversation": 0,
	}
	if got := DefaultBudgets(); !reflect.DeepEqual(got, wantBudgets) {
		t.Errorf("DefaultBudgets() = %v, want %v", got, wantBudgets)
	}
	wantCompactable := map[string]bool{
		"universal": false, "system": false, "memory": true, "agent": false,
		"mode": false, "rules": false, "permissions": false, "workspace": false,
		"skills": false, "tools": true, "session": true, "context": true,
		"user_context": false, "handoff": false, "conversation": true,
	}
	if got := DefaultCompactable(); !reflect.DeepEqual(got, wantCompactable) {
		t.Errorf("DefaultCompactable() = %v, want %v", got, wantCompactable)
	}
	if SlotHandoffMaxTokens != 1500 {
		t.Errorf("SlotHandoffMaxTokens = %d, want 1500", SlotHandoffMaxTokens)
	}
	if BudgetFraction != 0.80 {
		t.Errorf("BudgetFraction = %v, want 0.80", BudgetFraction)
	}
	if DefaultContextWindowSize != 200_000 {
		t.Errorf("DefaultContextWindowSize = %d, want 200000", DefaultContextWindowSize)
	}
	wantStages := []string{"drop_enrichment", "dedupe_tool_results", "summarize_oldest", "strip_tool_blocks"}
	var gotStages []string
	for _, s := range DefaultStages() {
		gotStages = append(gotStages, s.Name)
	}
	if !reflect.DeepEqual(gotStages, wantStages) {
		t.Errorf("DefaultStages names = %q, want %q", gotStages, wantStages)
	}
}
