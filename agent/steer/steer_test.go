package steer

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/util/router"
)

var (
	general = Slot{Name: "general", About: "whatever is in front"}
	notes   = Slot{Name: "notes", Module: "notes", Views: []string{"notes"}, Tools: []string{"notes_read"}, Instructions: "Keep notes short."}
	mail    = Slot{Name: "mail", Module: "mail", Tools: []string{"mail_list"}, Modes: []string{"triage"}}
	draft   = Slot{Name: "drafts", Module: "drafts", Views: []string{"drafts"}}
	all     = []Slot{notes, mail, draft}
)

func focused(view string) Situation { return Situation{Focused: &Focus{View: view}} }

func TestFocusSelector(t *testing.T) {
	ctx := context.Background()
	f := NewFocusSelector(general)
	if s := f.Select(ctx, Situation{}, all); s.Primary().Name != "general" || s.Why != "default" {
		t.Fatalf("empty situation: %+v", s)
	}
	if s := f.Select(ctx, focused("notes"), all); s.Primary().Name != "notes" || s.Why != "focus" {
		t.Fatalf("notes in front: %+v", s)
	}
	// A view of a module with no slot of its own selects the module's slot.
	if s := f.Select(ctx, focused("mail.thread"), all); s.Primary().Name != "mail" {
		t.Fatalf("module view: %+v", s)
	}
	// A pin holds while the same window stays in front, and gives way when another comes.
	f.Pin("drafts", "mail.thread")
	if s := f.Select(ctx, focused("mail.thread"), all); s.Primary().Name != "drafts" || s.Why != "pinned" {
		t.Fatalf("pinned: %+v", s)
	}
	if s := f.Select(ctx, focused("notes"), all); s.Primary().Name != "notes" || s.Why != "focus" {
		t.Fatalf("pin should give way: %+v", s)
	}
	// A mode adds the slots that name it.
	sit := focused("notes")
	sit.Mode = "triage"
	if s := f.Select(ctx, sit, all); !reflect.DeepEqual(s.Names(), []string{"notes", "mail"}) {
		t.Fatalf("mode slots: %+v", s.Names())
	}
	// Nothing in front keeps the current slot.
	if s := f.Select(ctx, Situation{}, all); s.Primary().Name != "notes" || s.Why != "kept" {
		t.Fatalf("kept: %+v", s)
	}
}

func TestPolicyJudge(t *testing.T) {
	p := Policy{FacetSlot: {Gate: GateAuto, Threshold: 0.7}, FacetPreset: {Gate: GateSuggest, Threshold: 0.8}, FacetAct: {Gate: GateShadow, Threshold: 0.5}}
	for _, tc := range []struct {
		f    Facet
		r    FacetResult
		want Outcome
		why  string
	}{
		{FacetSlot, FacetResult{Label: "notes", P: 0.9}, OutcomeApply, ""},
		{FacetSlot, FacetResult{Label: "notes", P: 0.6}, OutcomeRecord, "below threshold"},
		{FacetSlot, FacetResult{Abstained: true}, OutcomeRecord, "abstained"},
		{FacetPreset, FacetResult{Label: "focus", P: 0.85}, OutcomeSuggest, ""},
		{FacetAct, FacetResult{Label: "task", P: 0.99}, OutcomeRecord, "shadow"},
		{FacetAffect, FacetResult{Label: "rushed", P: 0.99}, OutcomeRecord, "shadow"}, // not in the policy
	} {
		v := p.Judge(tc.f, tc.r)
		if v.Outcome != tc.want || v.Why != tc.why {
			t.Errorf("%s %+v: got %s (%s), want %s (%s)", tc.f, tc.r, v.Outcome, v.Why, tc.want, tc.why)
		}
	}
	if err := DefaultPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Policy{{FacetSlot: {Gate: "maybe", Threshold: 0.5}}, {FacetSlot: {Gate: GateAuto, Threshold: 0}}, {FacetSlot: {Gate: GateAuto, Threshold: 1.5}}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	for f, fp := range DefaultPolicy() {
		if fp.Gate == GateAuto {
			t.Errorf("default policy auto-applies %s", f)
		}
	}
}

func TestRegistryLayers(t *testing.T) {
	reg, err := NewRegistry(DefaultPresets()...)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Default() != "general" {
		t.Fatalf("default %q", reg.Default())
	}
	if err := reg.Add(LayerModule, "", Preset{Name: "writing"}); err == nil {
		t.Fatal("a module preset with no module was accepted")
	}
	if err := reg.Add(LayerApp, "notes", Preset{Name: "writing"}); err == nil {
		t.Fatal("an app preset naming a source was accepted")
	}
	if err := reg.Add(LayerModule, "notes", Preset{Name: "Bad Name"}); err == nil {
		t.Fatal("a bad name was accepted")
	}
	// A module contributes a new preset, and overrides a default.
	must(t, reg.Add(LayerModule, "notes", Preset{Name: "writing", Instructions: "from notes"}))
	must(t, reg.Add(LayerModule, "notes", Preset{Name: "focus", Instructions: "notes focus"}))
	p, o, ok := reg.Get("writing")
	if !ok || p.Instructions != "from notes" || o.Layer != LayerModule || o.Source != "notes" {
		t.Fatalf("module preset: %+v %+v", p, o)
	}
	if p, _, _ := reg.Get("focus"); p.Instructions != "notes focus" {
		t.Fatalf("module should override the default: %+v", p)
	}
	// The app overrides both; a second module does not displace the first.
	must(t, reg.Add(LayerModule, "drafts", Preset{Name: "writing", Instructions: "from drafts"}))
	if p, _, _ := reg.Get("writing"); p.Instructions != "from notes" {
		t.Fatalf("first module should win among modules: %+v", p)
	}
	must(t, reg.Add(LayerApp, "", Preset{Name: "writing", Instructions: "from app"}))
	if p, o, _ := reg.Get("writing"); p.Instructions != "from app" || o.LayerName != "app" {
		t.Fatalf("app should win: %+v %+v", p, o)
	}
	reg.Remove(LayerApp, "", "writing")
	if p, _, _ := reg.Get("writing"); p.Instructions != "from notes" {
		t.Fatalf("after removing the app preset: %+v", p)
	}
	if !reflect.DeepEqual(reg.Names(), []string{"focus", "general", "research", "triage", "writing"}) {
		t.Fatalf("names %v", reg.Names())
	}
	if err := reg.SetDefault("nope"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("SetDefault unknown: %v", err)
	}
	// What Get returns is a copy.
	p, _, _ = reg.Get("research")
	p.Dispatch[0] = "changed"
	if q, _, _ := reg.Get("research"); q.Dispatch[0] != "subtask" {
		t.Fatal("Get returned shared state")
	}
}

func TestOnlyTheOwnerSetsTheMode(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	m := NewModes(reg)
	for _, k := range []ActorKind{ActorAgent, ActorHost, ActorReflex, ""} {
		if err := m.Set(Actor{Kind: k}, "focus"); !errors.Is(err, ErrNotOwner) {
			t.Errorf("%q set the mode: %v", k, err)
		}
	}
	if err := m.Set(Actor{Kind: ActorOwner}, "nope"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("unknown mode: %v", err)
	}
	must(t, m.Set(Actor{Kind: ActorOwner}, "focus"))
	if m.Current() != "focus" {
		t.Fatal(m.Current())
	}
	must(t, m.Set(Actor{Kind: ActorOwner}, ""))
	if m.Current() != "" {
		t.Fatal("not cleared")
	}
}

func TestResolvePrecedence(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	auto := Policy{FacetPreset: {Gate: GateAuto, Threshold: 0.8}}
	sure := Classification{FacetPreset: {Label: "triage", P: 0.95}}

	r := Resolve(reg, "focus", sure, auto)
	if r.Source != SourceDeliberate || r.Preset.Name != "focus" || r.Verdict.Outcome != OutcomeRecord || r.Verdict.Why != "deliberate mode" {
		t.Fatalf("deliberate should win: %+v", r)
	}
	if r := Resolve(reg, "", sure, auto); r.Source != SourceClassified || r.Preset.Name != "triage" {
		t.Fatalf("auto should apply: %+v", r)
	}
	if r := Resolve(reg, "", sure, DefaultPolicy()); r.Source != SourceDefault || r.Preset.Name != "general" || r.Verdict.Outcome != OutcomeSuggest {
		t.Fatalf("suggest should not apply: %+v", r)
	}
	if r := Resolve(reg, "", Classification{FacetPreset: {Label: "nope", P: 1}}, auto); r.Source != SourceDefault || r.Verdict.Why != "unknown label" {
		t.Fatalf("unknown label: %+v", r)
	}
	if r := Resolve(reg, "gone", nil, auto); r.Source != SourceDefault {
		t.Fatalf("an unregistered deliberate mode should fall through: %+v", r)
	}
	if r := Resolve(reg, "", nil, auto); r.Verdict != nil {
		t.Fatalf("no classifier, no verdict: %+v", r)
	}
}

func TestReflexValidation(t *testing.T) {
	v := NewVocabulary()
	must(t, v.Register("call_tool", nil))
	if err := v.Register(ActionRemind, nil); err == nil {
		t.Fatal("re-registering remind was accepted")
	}
	ok := []Reflex{
		{Name: "a", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"(?i)triage"}`), ActionKind: ActionRemind, Action: raw(`{"body":"x"}`)},
		{Name: "b", TriggerKind: TriggerPredicate, Trigger: raw(`{"kind":"attr","key":"mode","value":"focus"}`), ActionKind: ActionHalt, Action: raw(`{"message":"m"}`)},
		{Name: "c", TriggerKind: TriggerEvent, Trigger: raw(`{"topic":"notes.saved"}`), ActionKind: "call_tool", Action: raw(`{"tool":"t"}`)},
		{Name: "d", TriggerKind: TriggerFocus, Trigger: raw(`{"view":"notes"}`), ActionKind: ActionRemind, Action: raw(`{"body":"y"}`)},
	}
	for _, r := range ok {
		if err := r.Validate(v); err != nil {
			t.Errorf("%s: %v", r.Name, err)
		}
	}
	bad := []Reflex{
		{Name: "Bad", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"x"}`), ActionKind: ActionRemind, Action: raw(`{"body":"x"}`)},
		{Name: "a", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"("}`), ActionKind: ActionRemind, Action: raw(`{"body":"x"}`)},
		{Name: "a", TriggerKind: TriggerPredicate, Trigger: raw(`{"kind":"nope"}`), ActionKind: ActionRemind, Action: raw(`{"body":"x"}`)},
		{Name: "a", TriggerKind: TriggerEvent, Trigger: raw(`{}`), ActionKind: ActionRemind, Action: raw(`{"body":"x"}`)},
		{Name: "a", TriggerKind: "cron", Trigger: raw(`{}`), ActionKind: ActionRemind, Action: raw(`{"body":"x"}`)},
		{Name: "a", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"x"}`), ActionKind: "run_command", Action: raw(`{}`)},
		{Name: "a", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"x"}`), ActionKind: ActionRemind, Action: raw(`{"body":""}`)},
		{Name: "a", TriggerKind: TriggerEvent, Trigger: raw(`{"topic":"t"}`), ActionKind: ActionHalt, Action: raw(`{"message":"m"}`)},
		{Name: "a", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"x"}`), ActionKind: ActionRemind, Action: raw(`{"body":"x"}`), Cooldown: -1},
	}
	for i, r := range bad {
		if r.Validate(v) == nil {
			t.Errorf("bad[%d] accepted: %+v", i, r)
		}
	}
	if _, err := ParseReflexes([]byte(`[{"name":"a","trigger_kind":"text","trigger":{"pattern":"x"},"action_kind":"remind","action":{"body":"b"},"extra":1}]`), v); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if _, err := ParseReflexes([]byte(`[{"name":"a","trigger_kind":"text","trigger":{"pattern":"x"},"action_kind":"remind","action":{"body":"b"}},{"name":"a","trigger_kind":"text","trigger":{"pattern":"y"},"action_kind":"remind","action":{"body":"c"}}]`), v); err == nil {
		t.Fatal("a duplicate name was accepted")
	}
}

func TestReflexesFire(t *testing.T) {
	v := NewVocabulary()
	must(t, v.Register("call_tool", nil))
	x := NewReflexes(v)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	x.now = func() time.Time { return now }
	set := []Reflex{
		{Name: "remind-triage", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"(?i)triage"}`), ActionKind: ActionRemind, Action: raw(`{"body":"lead with decisions"}`), Cooldown: 60},
		{Name: "stop-1", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"^stop"}`), ActionKind: ActionHalt, Action: raw(`{"message":"first"}`)},
		{Name: "stop-2", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"^stop"}`), ActionKind: ActionHalt, Action: raw(`{"message":"second"}`)},
		{Name: "in-focus", TriggerKind: TriggerPredicate, Trigger: raw(`{"kind":"attr","key":"mode","value":"focus"}`), ActionKind: "call_tool", Action: raw(`{"tool":"mute"}`)},
		{Name: "saved", TriggerKind: TriggerEvent, Trigger: raw(`{"topic":"notes"}`), ActionKind: ActionRemind, Action: raw(`{"body":"a note was saved"}`)},
		{Name: "front", TriggerKind: TriggerFocus, Trigger: raw(`{"view":"mail"}`), ActionKind: "call_tool", Action: raw(`{"tool":"refresh"}`)},
	}
	ctx := context.Background()
	out, err := x.OnMessage(ctx, Message{Text: "Triage please"}, set)
	must(t, err)
	if !reflect.DeepEqual(out.Reminders, []string{"lead with decisions"}) || !reflect.DeepEqual(out.Names, []string{"remind-triage"}) {
		t.Fatalf("text trigger: %+v", out)
	}
	// Cooling down.
	now = now.Add(30 * time.Second)
	if out, _ := x.OnMessage(ctx, Message{Text: "triage again"}, set); len(out.Names) != 0 {
		t.Fatalf("fired inside its cooldown: %+v", out)
	}
	now = now.Add(31 * time.Second)
	if out, _ := x.OnMessage(ctx, Message{Text: "triage again"}, set); len(out.Names) != 1 {
		t.Fatalf("did not fire after its cooldown: %+v", out)
	}
	// The first halt answers; both are recorded as fired.
	if out, _ := x.OnMessage(ctx, Message{Text: "stop now"}, set); out.Halt != "first" || len(out.Names) != 2 {
		t.Fatalf("halt: %+v", out)
	}
	// A predicate over the situation; a host action comes back to the host.
	out, err = x.OnMessage(ctx, Message{Text: "hi", Sit: Situation{Mode: "focus"}}, set)
	must(t, err)
	if len(out.Actions) != 1 || out.Actions[0].Kind != "call_tool" || out.Actions[0].Reflex != "in-focus" {
		t.Fatalf("predicate action: %+v", out)
	}
	// An event's reminder waits for the next message; a focus action returns at once.
	if ev := x.OnEvent("notes.saved", set); len(ev.Reminders) != 0 || !reflect.DeepEqual(ev.Names, []string{"saved"}) {
		t.Fatalf("event: %+v", ev)
	}
	if ev := x.OnEvent("notesx.saved", set); len(ev.Names) != 0 {
		t.Fatalf("topic prefix must stop at a dot: %+v", ev)
	}
	if fo := x.OnFocus("mail", set); len(fo.Actions) != 1 || fo.Actions[0].Spec == nil {
		t.Fatalf("focus: %+v", fo)
	}
	out, _ = x.OnMessage(ctx, Message{Text: "hello"}, set)
	if !reflect.DeepEqual(out.Reminders, []string{"a note was saved"}) {
		t.Fatalf("waiting reminder: %+v", out)
	}
	// A broken trigger is reported, and the others still run.
	broken := append([]Reflex{{Name: "broken", TriggerKind: TriggerPredicate, Trigger: raw(`{"kind":"nope"}`)}}, set[1])
	out, err = x.OnMessage(ctx, Message{Text: "stop"}, broken)
	if err == nil || out.Halt != "first" {
		t.Fatalf("broken trigger: %v %+v", err, out)
	}
}

func TestMessageSubjectAndRules(t *testing.T) {
	sit := Situation{Focused: &Focus{View: "notes", Module: "notes"}, Mode: "focus", Input: " /open today's note "}
	s := MessageSubject("m1", sit, "notes")
	want := map[string]string{"text": "/open today's note", "slash": "open", "arg": "today's note", "view": "notes", "module": "notes", "slot": "notes", "mode": "focus"}
	if s.ID != "m1" || !reflect.DeepEqual(s.Fields, want) {
		t.Fatalf("subject: %+v", s)
	}
	rs := router.RuleSet{Version: "1", Strategy: router.FirstMatch, Rules: []router.Rule{
		{ID: "always", Target: "tool:a"},
		{ID: "focus-only", Target: "tool:b", Disabled: true},
		{ID: "off", Target: "tool:c", Disabled: true},
	}}
	got := ActiveRules(rs, Preset{Rules: []string{"focus-only"}})
	if got.Rules[1].Disabled || !got.Rules[2].Disabled || !rs.Rules[1].Disabled {
		t.Fatalf("active rules: %+v (input %+v)", got.Rules, rs.Rules)
	}
}

// R6 acceptance: a deliberate mode activates its reflexes and instructions.
func TestDeliberateModeActivatesItsReflexesAndInstructions(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	must(t, reg.Add(LayerApp, "", Preset{Name: "focus", Instructions: "Be brief.", Reflexes: []string{"hush"}, Slots: []string{"drafts"}, Tools: []string{"drafts_write"}}))
	s := New(reg, NewFocusSelector(general), NewVocabulary())
	hush := Reflex{Name: "hush", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"."}`), ActionKind: ActionRemind, Action: raw(`{"body":"hold non-urgent updates"}`)}
	in := Input{Sit: Situation{Focused: &Focus{View: "notes"}, Input: "what's next?"}, Slots: all, Reflexes: []Reflex{hush}}

	turn, err := s.Prepare(context.Background(), in)
	must(t, err)
	if turn.Mode.Source != SourceDefault || len(turn.Fired.Names) != 0 {
		t.Fatalf("before the mode: %+v", turn)
	}
	must(t, s.Modes.Set(Actor{Kind: ActorOwner}, "focus"))
	turn, err = s.Prepare(context.Background(), in)
	must(t, err)
	if turn.Mode.Source != SourceDeliberate || turn.Mode.Preset.Name != "focus" {
		t.Fatalf("mode: %+v", turn.Mode)
	}
	if !reflect.DeepEqual(turn.Fired.Reminders, []string{"hold non-urgent updates"}) {
		t.Fatalf("the mode's reflex did not fire: %+v", turn.Fired)
	}
	if turn.Instructions[0] != "Be brief." || turn.Instructions[1] != "Keep notes short." {
		t.Fatalf("instructions: %v", turn.Instructions)
	}
	if !reflect.DeepEqual(turn.Selection.Names(), []string{"notes", "drafts"}) || !reflect.DeepEqual(turn.Tools, []string{"drafts_write", "notes_read"}) {
		t.Fatalf("slots %v tools %v", turn.Selection.Names(), turn.Tools)
	}
	if turn.Decision.Situation.Mode != "focus" || turn.Decision.ModeSource != SourceDeliberate {
		t.Fatalf("decision: %+v", turn.Decision)
	}
}

// R6 acceptance: a classified suggestion surfaces but does not apply in suggest mode.
func TestClassifiedSuggestionDoesNotApply(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	s := New(reg, NewFocusSelector(general), NewVocabulary())
	cls := Classification{FacetPreset: {Label: "triage", P: 0.9}, FacetSlot: {Label: "mail", P: 0.9}, FacetAct: {Label: "task", P: 0.9}}
	turn, err := s.Prepare(context.Background(), Input{Sit: Situation{Focused: &Focus{View: "notes"}, Input: "go through my inbox"}, Slots: all, Classified: cls})
	must(t, err)
	if turn.Mode.Preset.Name != "general" || turn.Selection.Primary().Name != "notes" {
		t.Fatalf("a suggestion was applied: mode %s slot %s", turn.Mode.Preset.Name, turn.Selection.Primary().Name)
	}
	if len(turn.Suggestions) != 2 || turn.Suggestions[0].Facet != FacetPreset || turn.Suggestions[1].Facet != FacetSlot {
		t.Fatalf("suggestions: %+v", turn.Suggestions)
	}
	if len(turn.Decision.Verdicts) != 3 {
		t.Fatalf("verdicts: %+v", turn.Decision.Verdicts)
	}
	// The owner accepts the preset suggestion: it becomes the deliberate mode.
	if err := s.Accept(Actor{Kind: ActorAgent}, turn.Suggestions[0], ""); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("the agent accepted: %v", err)
	}
	must(t, s.Accept(Actor{Kind: ActorOwner}, turn.Suggestions[0], ""))
	must(t, s.Accept(Actor{Kind: ActorOwner}, turn.Suggestions[1], "notes"))
	turn, _ = s.Prepare(context.Background(), Input{Sit: Situation{Focused: &Focus{View: "notes"}, Input: "next"}, Slots: all})
	if turn.Mode.Source != SourceDeliberate || turn.Mode.Preset.Name != "triage" || turn.Selection.Primary().Name != "mail" {
		t.Fatalf("after accepting: %s %s %s", turn.Mode.Source, turn.Mode.Preset.Name, turn.Selection.Primary().Name)
	}

	// With the slot facet on auto, a confident slot leads.
	s2 := New(reg, NewFocusSelector(general), NewVocabulary())
	s2.Policy[FacetSlot] = FacetPolicy{Gate: GateAuto, Threshold: 0.7}
	turn, _ = s2.Prepare(context.Background(), Input{Sit: Situation{Focused: &Focus{View: "notes"}, Input: "inbox"}, Slots: all, Classified: Classification{FacetSlot: {Label: "mail", P: 0.9}}})
	if turn.Selection.Primary().Name != "mail" || turn.Selection.Why != "classified" {
		t.Fatalf("auto slot: %+v", turn.Selection)
	}
}

// R6 acceptance: a module-contributed preset loads and can be set.
func TestModulePresetLoads(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	must(t, reg.Add(LayerModule, "drafts", Preset{Name: "writing", Slots: []string{"drafts"}, Instructions: "Help the owner write."}))
	s := New(reg, NewFocusSelector(general), NewVocabulary())
	must(t, s.Modes.Set(Actor{Kind: ActorOwner}, "writing"))
	turn, err := s.Prepare(context.Background(), Input{Sit: Situation{Input: "start the intro"}, Slots: all})
	must(t, err)
	if turn.Mode.Origin.Source != "drafts" || !strings.Contains(strings.Join(turn.Instructions, "\n"), "Help the owner write.") || !turn.Selection.has("drafts") {
		t.Fatalf("module preset: %+v", turn)
	}
}

func TestTraceFields(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	s := New(reg, NewFocusSelector(general), NewVocabulary())
	turn, _ := s.Prepare(context.Background(), Input{Sit: Situation{Input: "hi", Source: "user"}, Slots: all})
	sit, cls, why, fired := turn.Decision.TraceFields()
	if cls != nil || why != "default" || fired == nil || len(fired) != 0 {
		t.Fatalf("no classifier: cls=%s why=%s fired=%v", cls, why, fired)
	}
	var got map[string]any
	must(t, json.Unmarshal(sit, &got))
	if got["mode"] != "general" || got["mode_source"] != "default" || got["source"] != "user" {
		t.Fatalf("situation: %s", sit)
	}
	turn, _ = s.Prepare(context.Background(), Input{Sit: Situation{Input: "hi"}, Slots: all, Classified: Classification{FacetAct: {Label: "chat", P: 0.4}}})
	if _, cls, _, _ := turn.Decision.TraceFields(); !strings.Contains(string(cls), `"verdicts"`) {
		t.Fatalf("classified: %s", cls)
	}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
