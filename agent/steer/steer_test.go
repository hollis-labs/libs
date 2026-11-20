package steer

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/agent/core/reflexes"
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
	if p, o, _ := reg.Get("writing"); p.Instructions != "from app" || o.Layer != LayerApp {
		t.Fatalf("app should win: %+v %+v", p, o)
	}
	must(t, reg.Remove(LayerApp, "", "writing"))
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

func TestOnlyOwnerControlsSetTheMode(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	s, owner, err := New(reg, NewFocusSelector(general), NewVocabulary())
	must(t, err)
	if err := owner.SetMode("nope"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("unknown mode: %v", err)
	}
	must(t, owner.SetMode("focus"))
	if st := s.Modes().State(); st.Name != "focus" || st.Since.IsZero() {
		t.Fatalf("state %+v", st)
	}
	must(t, owner.SetMode(""))
	if s.Modes().Current() != "" {
		t.Fatal("not cleared")
	}
	// Owner controls never come from data.
	var decoded struct{ Owner *OwnerControls }
	if err := json.Unmarshal([]byte(`{"Owner":{}}`), &decoded); !errors.Is(err, ErrNotOwnerControls) {
		t.Fatalf("decoded owner controls: %v", err)
	}
	if _, _, err := New(nil, NewFocusSelector(general), NewVocabulary()); err == nil {
		t.Fatal("New without a registry")
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
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	cool := NewMemoryCooldowns()
	x, err := NewReflexes(v, WithClock(func() time.Time { return now }), WithCooldowns(cool))
	must(t, err)
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
	if ev, _ := x.OnEvent(ctx, "notes.saved", set); len(ev.Reminders) != 0 || !reflect.DeepEqual(ev.Names, []string{"saved"}) {
		t.Fatalf("event: %+v", ev)
	}
	if ev, _ := x.OnEvent(ctx, "notesx.saved", set); len(ev.Names) != 0 || ev.Names == nil {
		t.Fatalf("topic prefix must stop at a dot, and none fired is []: %+v", ev)
	}
	if fo, _ := x.OnFocus(ctx, "mail", set); len(fo.Actions) != 1 || fo.Actions[0].Spec == nil {
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
	// An action kind outside the vocabulary never comes back as an action.
	rogue := Reflex{Name: "rogue", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"go"}`), ActionKind: "run_command", Action: raw(`{"command":"rm"}`)}
	out, err = x.OnMessage(ctx, Message{Text: "go"}, []Reflex{rogue})
	if err == nil || len(out.Actions) != 0 || len(out.Names) != 0 {
		t.Fatalf("unknown action kind: %v %+v", err, out)
	}
	// Text is trimmed before triggers run.
	if out, _ := x.OnMessage(ctx, Message{Text: "  stop  "}, set[1:2]); out.Halt != "first" {
		t.Fatalf("untrimmed: %+v", out)
	}
	// Cooldowns persist through the store: a fresh runner over a restored snapshot is still
	// cooling down.
	snap := cool.Snapshot()
	again := NewMemoryCooldowns()
	again.Restore(snap)
	y, err := NewReflexes(v, WithClock(func() time.Time { return now.Add(10 * time.Second) }), WithCooldowns(again))
	must(t, err)
	if out, _ := y.OnMessage(ctx, Message{Text: "triage"}, set); len(out.Names) != 0 {
		t.Fatalf("a restart forgot the cooldown: %+v", out)
	}
}

func TestSignalsAndStateFill(t *testing.T) {
	v := NewVocabulary()
	var seen string
	x, err := NewReflexes(v, WithAgentID("agent-1"), WithStateFill(func(st *reflexes.State, sit Situation) {
		seen = st.AgentID
		st.MailUnreadCount = 3
	}))
	must(t, err)
	set := []Reflex{
		{Name: "mail", TriggerKind: TriggerPredicate, Trigger: raw(`{"kind":"mail_unread_count","op":">=","value":2}`), ActionKind: ActionRemind, Action: raw(`{"body":"mail waiting"}`)},
		{Name: "busy", TriggerKind: TriggerPredicate, Trigger: raw(`{"kind":"attr","key":"signal.calendar.busy","value":"yes"}`), ActionKind: ActionRemind, Action: raw(`{"body":"in a meeting"}`)},
		{Name: "focus-only", TriggerKind: TriggerPredicate, Trigger: raw(`{"kind":"attr","key":"mode","value":"focus"}`), ActionKind: ActionRemind, Action: raw(`{"body":"focused"}`)},
	}
	// A signal named "mode" cannot stand in for the real mode.
	out, err := x.OnMessage(context.Background(), Message{Text: "hi", Sit: Situation{Signals: map[string]string{"calendar.busy": "yes", "mode": "focus"}}}, set)
	must(t, err)
	if seen != "agent-1" || !reflect.DeepEqual(out.Names, []string{"mail", "busy"}) {
		t.Fatalf("agent %q fired %v", seen, out.Names)
	}
	if _, err := NewReflexes(nil); err == nil {
		t.Fatal("NewReflexes without a vocabulary")
	}
	if _, err := NewReflexes(v, WithPolicy(DefaultPolicy())); err == nil {
		t.Fatal("NewReflexes accepted a policy")
	}
}

type failingCooldowns struct{ get, set error }

func (f failingCooldowns) LastFired(context.Context, string) (time.Time, bool, error) {
	return time.Time{}, false, f.get
}
func (f failingCooldowns) SetFired(context.Context, string, time.Time) error { return f.set }

func TestCooldownStoreErrorsAreReported(t *testing.T) {
	r := Reflex{Name: "r", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"."}`), ActionKind: ActionRemind, Action: raw(`{"body":"b"}`), Cooldown: 60}
	for _, store := range []failingCooldowns{{get: errors.New("read")}, {set: errors.New("write")}} {
		x, err := NewReflexes(NewVocabulary(), WithCooldowns(store))
		must(t, err)
		out, err := x.OnMessage(context.Background(), Message{Text: "hi"}, []Reflex{r})
		if err == nil || len(out.Names) != 0 {
			t.Fatalf("store %+v: %v %+v", store, err, out)
		}
	}
}

func TestBadConfidenceIsAbstained(t *testing.T) {
	p := Policy{FacetSlot: {Gate: GateAuto, Threshold: 0.7}}
	for _, conf := range []float64{math.NaN(), -0.1, 1.5} {
		v := p.Judge(FacetSlot, FacetResult{Label: "notes", P: conf})
		if v.Outcome != OutcomeRecord || v.Why != "abstained" || v.P != 0 {
			t.Errorf("confidence %v: %+v", conf, v)
		}
	}
	reg, _ := NewRegistry(DefaultPresets()...)
	s, _ := newSteer(t, reg)
	turn, _ := s.Prepare(context.Background(), Input{Sit: Situation{Input: "x"}, Slots: all,
		Classified: Classification{FacetSlot: {Label: "mail", P: math.NaN()}, "mood": {Label: "SECRET", P: 0.9}}})
	_, cls, _, _ := turn.Decision.TraceFields()
	if cls == nil || strings.Contains(string(cls), "mood") || strings.Contains(string(cls), "SECRET") {
		t.Fatalf("trace classified: %s", cls)
	}
}

func TestMessageSubjectAndRules(t *testing.T) {
	sit := Situation{Focused: &Focus{View: "notes", Module: "notes"}, Mode: "focus", Input: " /open today's note "}
	s := MessageSubject("m1", sit, "notes")
	want := map[string]string{"text": "/open today's note", "slash": "open", "arg": "today's note", "view": "notes", "module": "notes", "slot": "notes", "mode": "focus"}
	if s.ID != "m1" || !reflect.DeepEqual(s.Fields, want) {
		t.Fatalf("subject: %+v", s)
	}
	for _, tc := range []struct{ in, slash, arg string }{
		{"/Open Notes", "open", "Notes"},
		{"/a.b-c_d rest of it", "a.b-c_d", "rest of it"},
		{"/go", "go", ""},
		{"/", "", ""},
		{"/ spaced", "", ""},
		{"say /go", "", ""},
		{"/emoji✓ tail", "emoji", "✓ tail"},
	} {
		f := MessageSubject("m", Situation{Input: tc.in}, "").Fields
		if f["slash"] != tc.slash || f["arg"] != tc.arg {
			t.Errorf("%q: slash %q arg %q, want %q %q", tc.in, f["slash"], f["arg"], tc.slash, tc.arg)
		}
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

func newSteer(t *testing.T, reg *Registry, opts ...Option) (*Steer, *OwnerControls) {
	t.Helper()
	s, owner, err := New(reg, NewFocusSelector(general), NewVocabulary(), opts...)
	must(t, err)
	return s, owner
}

// R6 acceptance: a deliberate mode activates its reflexes and instructions.
func TestDeliberateModeActivatesItsReflexesAndInstructions(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	must(t, reg.Add(LayerApp, "", Preset{Name: "focus", Instructions: "Be brief.", Reflexes: []string{"hush"}, Slots: []string{"drafts"}, Tools: []string{"drafts_write"}}))
	s, owner := newSteer(t, reg)
	hush := Reflex{Name: "hush", TriggerKind: TriggerText, Trigger: raw(`{"pattern":"."}`), ActionKind: ActionRemind, Action: raw(`{"body":"hold non-urgent updates"}`)}
	in := Input{Sit: Situation{Focused: &Focus{View: "notes"}, Input: "what's next?"}, Slots: all, Reflexes: []Reflex{hush}}

	turn, err := s.Prepare(context.Background(), in)
	must(t, err)
	if turn.Mode.Source != SourceDefault || len(turn.Fired.Names) != 0 || turn.Decision.Situation.Mode != "" {
		t.Fatalf("before the mode: %+v", turn)
	}
	must(t, owner.SetMode("focus"))
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

// The default preset is not a mode: it neither selects mode slots nor matches mode
// conditions.
func TestDefaultPresetIsNotAMode(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	s, _ := newSteer(t, reg)
	generalist := Slot{Name: "everything", Modes: []string{"general"}}
	turn, err := s.Prepare(context.Background(), Input{Sit: Situation{Input: "hi"}, Slots: []Slot{generalist}})
	must(t, err)
	if turn.Selection.has("everything") || turn.Decision.Mode != "general" || turn.Decision.Situation.Mode != "" {
		t.Fatalf("default as a mode: %+v", turn.Decision)
	}
}

// R6 acceptance: a classified suggestion surfaces but does not apply in suggest mode.
func TestClassifiedSuggestionDoesNotApply(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	s, owner := newSteer(t, reg)
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
	// Only a suggestion for a known label can be accepted.
	forged := turn.Suggestions[0]
	forged.Outcome = OutcomeRecord
	if owner.Accept(forged, "", all) == nil {
		t.Fatal("a recorded verdict was accepted")
	}
	unknown := turn.Suggestions[1]
	unknown.Label = "nope"
	if owner.Accept(unknown, "", all) == nil {
		t.Fatal("an unknown slot was accepted")
	}
	must(t, owner.Accept(turn.Suggestions[0], "", all))
	must(t, owner.Accept(turn.Suggestions[1], "notes", all))
	turn, _ = s.Prepare(context.Background(), Input{Sit: Situation{Focused: &Focus{View: "notes"}, Input: "next"}, Slots: all})
	if turn.Mode.Source != SourceDeliberate || turn.Mode.Preset.Name != "triage" || turn.Selection.Primary().Name != "mail" {
		t.Fatalf("after accepting: %s %s %s", turn.Mode.Source, turn.Mode.Preset.Name, turn.Selection.Primary().Name)
	}

	// With the slot facet on auto, a confident slot leads, but not for an empty message.
	s2, _ := newSteer(t, reg, WithPolicy(Policy{FacetSlot: {Gate: GateAuto, Threshold: 0.7}}))
	slotCls := Classification{FacetSlot: {Label: "mail", P: 0.9}}
	turn, _ = s2.Prepare(context.Background(), Input{Sit: Situation{Focused: &Focus{View: "notes"}, Input: "inbox"}, Slots: all, Classified: slotCls})
	if turn.Selection.Primary().Name != "mail" || turn.Selection.Why != "classified" {
		t.Fatalf("auto slot: %+v", turn.Selection)
	}
	turn, _ = s2.Prepare(context.Background(), Input{Sit: Situation{Focused: &Focus{View: "notes"}, Input: "  "}, Slots: all, Classified: slotCls})
	if turn.Selection.Primary().Name != "notes" || len(turn.Suggestions) != 0 || turn.Decision.Verdicts[0].Why != "no input" {
		t.Fatalf("empty input: %+v %+v", turn.Selection, turn.Decision.Verdicts)
	}
}

func TestPolicyIsValidated(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	if _, _, err := New(reg, NewFocusSelector(general), NewVocabulary(), WithPolicy(Policy{FacetSlot: {Gate: GateAuto, Threshold: 0.3}})); err == nil {
		t.Fatal("an auto gate below the floor was accepted")
	}
	s, _ := newSteer(t, reg)
	if s.SetPolicy(Policy{FacetSlot: {Gate: "sometimes", Threshold: 0.9}}) == nil {
		t.Fatal("an unknown gate was accepted")
	}
	p := s.Policy()
	p[FacetSlot] = FacetPolicy{Gate: GateAuto, Threshold: 0.9}
	if s.Policy()[FacetSlot].Gate == GateAuto {
		t.Fatal("Policy returned shared state")
	}
	must(t, s.SetPolicy(p))
	if s.Policy()[FacetSlot].Gate != GateAuto {
		t.Fatal("SetPolicy did not apply")
	}
	// An invalid gate in a policy built by hand is shadowed when judged.
	if v := (Policy{FacetSlot: {Gate: "sometimes", Threshold: 0.1}}).Judge(FacetSlot, FacetResult{Label: "x", P: 1}); v.Outcome != OutcomeRecord || v.Why != "shadow" {
		t.Fatalf("invalid gate: %+v", v)
	}
}

// R6 acceptance: a module-contributed preset loads and can be set.
func TestModulePresetLoads(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	must(t, reg.Add(LayerModule, "drafts", Preset{Name: "writing", Slots: []string{"drafts"}, Instructions: "Help the owner write."}))
	s, owner := newSteer(t, reg)
	must(t, owner.SetMode("writing"))
	turn, err := s.Prepare(context.Background(), Input{Sit: Situation{Input: "start the intro"}, Slots: all})
	must(t, err)
	if turn.Mode.Origin.Source != "drafts" || !strings.Contains(strings.Join(turn.Instructions, "\n"), "Help the owner write.") || !turn.Selection.has("drafts") {
		t.Fatalf("module preset: %+v", turn)
	}
}

func TestTraceFieldsCarryNoPrompt(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	s, _ := newSteer(t, reg)
	sit := Situation{Input: "SECRET-INPUT", Source: "user", Layout: "desk",
		Focused: &Focus{View: "notes", Module: "notes", Title: "SECRET-TITLE", Selection: "SECRET-SELECTION"},
		Windows: []Window{{ID: "w1", View: "notes", Title: "SECRET-TITLE"}, {ID: "w2", View: "mail", Title: "SECRET-MAIL"}},
		Signals: map[string]string{"k": "SECRET-SIGNAL"}}
	turn, _ := s.Prepare(context.Background(), Input{Sit: sit, Slots: all})
	situation, cls, why, fired := turn.Decision.TraceFields()
	if strings.Contains(string(situation), "SECRET") {
		t.Fatalf("the trace situation carries text: %s", situation)
	}
	if cls != nil || why != "focus" || fired == nil || len(fired) != 0 {
		t.Fatalf("no classifier: cls=%s why=%s fired=%v", cls, why, fired)
	}
	var got TraceSituation
	must(t, json.Unmarshal(situation, &got))
	if got.Mode != "general" || got.ModeSource != "default" || got.Source != "user" || got.View != "notes" || !reflect.DeepEqual(got.Views, []string{"notes", "mail"}) || got.Layout != "desk" {
		t.Fatalf("situation: %+v", got)
	}
	turn, _ = s.Prepare(context.Background(), Input{Sit: Situation{Input: "hi"}, Slots: all, Classified: Classification{FacetAct: {Label: "chat", P: 0.4}}})
	if _, cls, _, _ := turn.Decision.TraceFields(); !strings.Contains(string(cls), `"verdicts"`) {
		t.Fatalf("classified: %s", cls)
	}
}

func TestRegistryChecksAndGuards(t *testing.T) {
	if _, err := NewRegistry(Preset{Name: "a"}, Preset{Name: "a"}); err == nil {
		t.Fatal("a duplicate default was accepted")
	}
	reg, _ := NewRegistry(DefaultPresets()...)
	if err := reg.Remove(LayerDefault, "", "general"); err == nil {
		t.Fatal("the default preset was removed")
	}
	must(t, reg.Add(LayerApp, "", Preset{Name: "general", About: "app general"}))
	must(t, reg.Remove(LayerApp, "", "general")) // the shipped one remains
	must(t, reg.Add(LayerApp, "", Preset{Name: "ops", Slots: []string{"mail", "ghost"}, Reflexes: []string{"hush", "nope"}, Rules: []string{"r1", "r2"}}))
	errs := reg.Check(all, []Reflex{{Name: "hush"}}, router.RuleSet{Rules: []router.Rule{{ID: "r1"}}})
	if len(errs) != 3 {
		t.Fatalf("check: %v", errs)
	}
	for _, l := range []Layer{LayerDefault, LayerModule, LayerApp} {
		b, err := json.Marshal(Origin{Layer: l})
		must(t, err)
		var o Origin
		must(t, json.Unmarshal(b, &o))
		if o.Layer != l {
			t.Fatalf("layer %v round-tripped to %v (%s)", l, o.Layer, b)
		}
	}
	var bad Layer
	if bad.UnmarshalText([]byte("vendor")) == nil {
		t.Fatal("an unknown layer was accepted")
	}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A selector that is not a FocusSelector can still take accepted slot suggestions.
type pinSelector struct {
	*FocusSelector
	pinned []string
}

func (p *pinSelector) Pin(name, view string) {
	p.pinned = append(p.pinned, name+"@"+view)
	p.FocusSelector.Pin(name, view)
}

func TestAcceptUsesAnyPinner(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	sel := &pinSelector{FocusSelector: NewFocusSelector(general)}
	_, owner, err := New(reg, sel, NewVocabulary())
	must(t, err)
	must(t, owner.Accept(Verdict{Facet: FacetSlot, Label: "mail", Outcome: OutcomeSuggest}, "notes", all))
	if !reflect.DeepEqual(sel.pinned, []string{"mail@notes"}) {
		t.Fatalf("pinned %v", sel.pinned)
	}
}

// A deliberate mode whose preset was removed falls back to the default, and the decision
// still says what was set.
func TestMissingDeliberateModeIsRecorded(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	must(t, reg.Add(LayerApp, "", Preset{Name: "temp"}))
	s, owner := newSteer(t, reg)
	must(t, owner.SetMode("temp"))
	must(t, reg.Remove(LayerApp, "", "temp"))
	turn, err := s.Prepare(context.Background(), Input{Sit: Situation{Input: "hi"}, Slots: all})
	must(t, err)
	if turn.Decision.ModeSource != SourceDefault || turn.Decision.Deliberate != "temp" {
		t.Fatalf("decision %+v", turn.Decision)
	}
	situation, _, _, _ := turn.Decision.TraceFields()
	if !strings.Contains(string(situation), `"deliberate":"temp"`) {
		t.Fatalf("trace: %s", situation)
	}
}

// Prepare skips and reports a reflex that is not valid, such as a halt with no message.
func TestPrepareValidatesReflexes(t *testing.T) {
	reg, _ := NewRegistry(DefaultPresets()...)
	s, _ := newSteer(t, reg)
	bad := Reflex{Name: "silent-halt", Enabled: true, TriggerKind: TriggerText, Trigger: raw(`{"pattern":"."}`), ActionKind: ActionHalt, Action: raw(`{}`)}
	ok := Reflex{Name: "note", Enabled: true, TriggerKind: TriggerText, Trigger: raw(`{"pattern":"."}`), ActionKind: ActionRemind, Action: raw(`{"body":"b"}`)}
	turn, err := s.Prepare(context.Background(), Input{Sit: Situation{Input: "hi"}, Reflexes: []Reflex{bad, ok}})
	if err == nil || !strings.Contains(err.Error(), "silent-halt") || turn.Fired.Halt != "" || !reflect.DeepEqual(turn.Fired.Names, []string{"note"}) {
		t.Fatalf("err %v fired %+v", err, turn.Fired)
	}
}
