package steer

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// Selector picks the active slots. Implementations must be quick and safe for concurrent use.
type Selector interface {
	Select(ctx context.Context, sit Situation, all []Slot) Selection
}

// FocusSelector selects by what is in front. The window in front selects a slot; a mode adds
// the slots that name it; a slot pinned with [FocusSelector.Pin] holds until the window in
// front selects another; with none of those, the slot stays where it was, or is the
// fallback.
type FocusSelector struct {
	fallback Slot

	mu      sync.Mutex
	current string
	pinned  bool
	// lastView is the view in front when current was chosen, so a pin holds only until the
	// owner moves to a different window.
	lastView string
}

// NewFocusSelector returns a selector that starts in, and falls back to, fallback.
func NewFocusSelector(fallback Slot) *FocusSelector {
	return &FocusSelector{fallback: fallback, current: fallback.Name}
}

// Fallback is the slot used when nothing more specific is.
func (f *FocusSelector) Fallback() Slot { return f.fallback }

// Pin makes name the primary slot until the window in front selects another. "" unpins.
func (f *FocusSelector) Pin(name, view string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		f.pinned = false
		return
	}
	f.current, f.pinned, f.lastView = name, true, view
}

// Select implements Selector.
func (f *FocusSelector) Select(_ context.Context, sit Situation, all []Slot) Selection {
	f.mu.Lock()
	defer f.mu.Unlock()
	view := ""
	if sit.Focused != nil {
		view = sit.Focused.View
	}
	why := "kept"
	if byView := forView(all, view); byView != "" {
		if !f.pinned || view != f.lastView {
			why = "focus"
			f.current, f.pinned, f.lastView = byView, false, view
		}
	}
	if f.pinned {
		why = "pinned"
	}
	sel := Selection{Why: why}
	if s, ok := f.named(all, f.current); ok {
		sel.Slots = append(sel.Slots, s)
	} else {
		f.current = f.fallback.Name
		sel.Slots = append(sel.Slots, f.fallback)
	}
	if sel.Why == "kept" && f.current == f.fallback.Name {
		sel.Why = "default"
	}
	if sit.Mode != "" {
		for _, s := range all {
			if slices.Contains(s.Modes, sit.Mode) && !sel.has(s.Name) {
				sel.Slots = append(sel.Slots, s)
			}
		}
		if len(sel.Slots) > 1 && (sel.Why == "kept" || sel.Why == "default") {
			sel.Why = "mode"
		}
	}
	return sel
}

func (f *FocusSelector) named(all []Slot, name string) (Slot, bool) {
	if name == f.fallback.Name {
		return f.fallback, true
	}
	return Named(all, name)
}

// forView is the slot a view in front selects, or "". A view "<module>.<name>" with no slot
// of its own selects its module's slot (a slot of that module that lists no views).
func forView(all []Slot, view string) string {
	if view == "" {
		return ""
	}
	for _, s := range all {
		if slices.Contains(s.Views, view) {
			return s.Name
		}
	}
	mod, _, _ := strings.Cut(view, ".")
	for _, s := range all {
		if s.Module == mod && len(s.Views) == 0 {
			return s.Name
		}
	}
	return ""
}

// leadWith puts the slot first, keeping the others in order after it.
func leadWith(sel Selection, lead Slot, why string) Selection {
	out := Selection{Slots: []Slot{lead}, Why: why}
	for _, s := range sel.Slots {
		if s.Name != lead.Name {
			out.Slots = append(out.Slots, s)
		}
	}
	return out
}
