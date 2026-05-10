package plugin

import (
	"context"
	"testing"
)

// fakeEventHook exercises the EventHook.PluginID contract.
type fakeEventHook struct {
	pluginID string
	types    []string
	calls    int
}

func (h *fakeEventHook) Handle(ctx context.Context, event Event) error {
	h.calls++
	return nil
}

func (h *fakeEventHook) EventTypes() []string { return h.types }
func (h *fakeEventHook) PluginID() string     { return h.pluginID }

func TestEventHookPluginID(t *testing.T) {
	var _ EventHook = (*fakeEventHook)(nil) // interface conformance

	h := &fakeEventHook{pluginID: "bookmarks", types: []string{"message.sent"}}
	if got := h.PluginID(); got != "bookmarks" {
		t.Errorf("PluginID() = %q, want %q", got, "bookmarks")
	}
	if len(h.EventTypes()) != 1 || h.EventTypes()[0] != "message.sent" {
		t.Errorf("EventTypes() = %v", h.EventTypes())
	}

	if err := h.Handle(context.Background(), Event{Type: "message.sent"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if h.calls != 1 {
		t.Errorf("calls = %d, want 1", h.calls)
	}
}

func TestUIComponentTypeConstants(t *testing.T) {
	// Guard against accidental renames — subprocess wire format depends
	// on these literal values.
	cases := map[UIComponentType]string{
		UIComponentTypeWidget:   "widget",
		UIComponentTypeEnvelope: "envelope",
		UIComponentTypeAction:   "action",
		UIComponentTypeWorkflow: "workflow",
		UIComponentTypeView:     "view",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("UIComponentType %q stringified as %q", want, string(got))
		}
	}
}
