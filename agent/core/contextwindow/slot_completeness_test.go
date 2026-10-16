package contextwindow

import "testing"

// TestDefaults_CompleteForSlotOrder asserts DefaultBudgets and
// DefaultCompactable each carry exactly one entry per name in SlotOrder, and
// no extras. NewContextWindowWithBudgetPct does plain map lookups, so a slot
// missing from either map silently becomes unbounded (MaxTokens 0) and
// non-compactable, the two things you most want to control on a new slot.
func TestDefaults_CompleteForSlotOrder(t *testing.T) {
	inOrder := make(map[string]bool, len(SlotOrder))
	for _, name := range SlotOrder {
		if inOrder[name] {
			t.Errorf("SlotOrder lists %q more than once", name)
		}
		inOrder[name] = true
	}

	budgets := DefaultBudgets()
	compactable := DefaultCompactable()

	for _, name := range SlotOrder {
		if _, ok := budgets[name]; !ok {
			t.Errorf("DefaultBudgets has no entry for slot %q", name)
		}
		if _, ok := compactable[name]; !ok {
			t.Errorf("DefaultCompactable has no entry for slot %q", name)
		}
	}
	for name := range budgets {
		if !inOrder[name] {
			t.Errorf("DefaultBudgets has extra entry %q not in SlotOrder", name)
		}
	}
	for name := range compactable {
		if !inOrder[name] {
			t.Errorf("DefaultCompactable has extra entry %q not in SlotOrder", name)
		}
	}
	if len(budgets) != len(SlotOrder) || len(compactable) != len(SlotOrder) {
		t.Errorf("sizes: SlotOrder=%d DefaultBudgets=%d DefaultCompactable=%d, want all equal",
			len(SlotOrder), len(budgets), len(compactable))
	}
}
