package reflexspec

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func codes(err error) []string {
	var out []string
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			var se *Error
			if errors.As(e, &se) {
				out = append(out, se.Field+"="+se.Code)
			}
		}
		return out
	}
	var se *Error
	if errors.As(err, &se) {
		out = append(out, se.Field+"="+se.Code)
	}
	return out
}

func has(err error, want string) bool {
	for _, c := range codes(err) {
		if c == want {
			return true
		}
	}
	return false
}

// validLeaf is one accepted node per leaf kind. TestEveryLeafKindHasValidExample
// keeps it in step with the grammar table.
var validLeaf = map[string]string{ //nolint:gosec // "tokens" in a key name, not a credential
	"tool_calls_window":       `{"kind":"tool_calls_window","window":3,"op":"=","value":0}`,
	"cache_read_window":       `{"kind":"cache_read_window","op":">=","value":100}`,
	"input_tokens_window":     `{"kind":"input_tokens_window","window":5,"op":"<","value":5}`,
	"output_growth_window":    `{"kind":"output_growth_window","window":3,"factor":1.5}`,
	"regex_match_window":      `{"kind":"regex_match_window","window":2,"pattern":"(?i)\\bdone\\b"}`,
	"user_regex_window":       `{"kind":"user_regex_window","window":1,"pattern":"sprint"}`,
	"text_regex_window":       `{"kind":"text_regex_window","pattern":"x","scope":"user"}`,
	"entity_mention_window":   `{"kind":"entity_mention_window","entity":"url"}`,
	"tool_name_window":        `{"kind":"tool_name_window","mode":"none","names":["wiki_search"]}`,
	"envelope_type_window":    `{"kind":"envelope_type_window","pattern":"^task_"}`,
	"mail_unread_count":       `{"kind":"mail_unread_count","op":">","value":0}`,
	"identical_output_window": `{"kind":"identical_output_window","window":3}`,
	"prefix_pressure":         `{"kind":"prefix_pressure","ratio":0.85,"context_window":200000}`,
	"attr":                    `{"kind":"attr","key":"scope","value":"open","op":"!="}`,
}

func TestEveryLeafKindHasValidExample(t *testing.T) {
	for _, k := range PredicateKinds() {
		if k == KindAND || k == KindOR {
			continue
		}
		spec, ok := validLeaf[k]
		if !ok {
			t.Errorf("leaf kind %q has no valid example in this test", k)
			continue
		}
		if err := ValidateTriggerSpec("predicate", spec); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	for k := range validLeaf {
		if _, ok := leafRules[k]; !ok {
			t.Errorf("example for %q, which is not a grammar kind", k)
		}
	}
}

// TestMisspelledPredicateKindIsRejected is the regression for the motivating
// defect: Nanite's validateReflexDefinition only checks that trigger_spec is
// JSON, so a kind one letter short is accepted there and silently never
// fires. This validator must reject it, at the top level and inside AND/OR.
func TestMisspelledPredicateKindIsRejected(t *testing.T) {
	for _, k := range PredicateKinds() {
		typos := []string{k[:len(k)-1], k + "s", strings.ToUpper(k) + "_", " " + k}
		if k != strings.ToLower(k) {
			typos = append(typos, strings.ToLower(k))
		}
		for _, typo := range typos {
			if _, known := leafRules[typo]; known || typo == KindAND || typo == KindOR {
				continue
			}
			body, _ := json.Marshal(map[string]any{"kind": typo, "window": 1, "pattern": "x", "key": "k", "clauses": []any{}})
			err := ValidateTriggerSpec("predicate", string(body))
			if !has(err, "trigger_spec.kind="+CodeUnknownPredicateKind) {
				t.Errorf("top-level %q: got %v", typo, codes(err))
			}
			nested := `{"kind":"AND","clauses":[` + validLeaf["attr"] + `,{"kind":"OR","clauses":[{"kind":` + string(mustJSON(typo)) + `}]}]}`
			err = ValidateTriggerSpec("predicate", nested)
			if !has(err, "trigger_spec.clauses[1].clauses[0].kind="+CodeUnknownPredicateKind) {
				t.Errorf("nested %q: got %v", typo, codes(err))
			}
		}
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestRegression_NaniteAcceptedDefinitionIsRejected(t *testing.T) {
	// Accepted by Nanite's validateReflexDefinition when run at HEAD 6443d0fd
	// (empty errs); see README "Verified vs read".
	d := Definition{
		Name:        "probe",
		TriggerKind: "predicate",
		TriggerSpec: `{"kind":"tool_call_window","window":1,"op":"=","value":0}`,
		ActionKind:  "halt_session",
		ActionSpec:  `{"reason":"x"}`,
		Status:      "active",
	}
	errs := Validate(d)
	if len(errs) != 1 || !has(errors.Join(errs...), "trigger_spec.kind=unknown_predicate_kind") {
		t.Fatalf("got %v", errs)
	}
}

func TestLeafRequiredFields(t *testing.T) {
	cases := []struct{ name, kind, spec, want string }{
		{"regex_match_window no pattern", "predicate", `{"kind":"regex_match_window","window":3}`, "trigger_spec.pattern=required"},
		{"user_regex_window no pattern", "predicate", `{"kind":"user_regex_window"}`, "trigger_spec.pattern=required"},
		{"text_regex_window empty pattern", "predicate", `{"kind":"text_regex_window","pattern":""}`, "trigger_spec.pattern=required"},
		{"attr no key", "predicate", `{"kind":"attr","value":"x"}`, "trigger_spec.key=required"},
		{"attr empty key", "predicate", `{"kind":"attr","key":""}`, "trigger_spec.key=required"},
		{"tool_name_window neither", "predicate", `{"kind":"tool_name_window","window":2}`, "trigger_spec.names=required"},
		{"envelope_type_window blank names", "predicate", `{"kind":"envelope_type_window","names":" , "}`, "trigger_spec.names=required"},
		{"entity_mention_window neither", "predicate", `{"kind":"entity_mention_window"}`, "trigger_spec.entity=required"},
		{"AND no clauses key", "predicate", `{"kind":"AND"}`, "trigger_spec.clauses=required"},
		{"OR empty clauses", "predicate", `{"kind":"OR","clauses":[]}`, "trigger_spec.clauses=required"},
		{"node without kind", "predicate", `{"window":3}`, "trigger_spec.kind=required"},
		{"kind not a string", "predicate", `{"kind":7}`, "trigger_spec.kind=wrong_type"},
		{"event no name", "event", `{}`, "trigger_spec.name=required"},
		{"interval no ticks", "interval", `{}`, "trigger_spec.every_n_ticks=required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateTriggerSpec(c.kind, c.spec); !has(err, c.want) {
				t.Fatalf("want %s, got %v", c.want, codes(err))
			}
		})
	}
}

func TestLeafValueRules(t *testing.T) {
	cases := []struct{ name, kind, spec, want string }{
		{"bad numeric op", "predicate", `{"kind":"tool_calls_window","op":"=>"}`, "trigger_spec.op=invalid_value"},
		{"attr op < is not allowed", "predicate", `{"kind":"attr","key":"k","op":"<"}`, "trigger_spec.op=invalid_value"},
		{"window zero", "predicate", `{"kind":"tool_calls_window","window":0}`, "trigger_spec.window=out_of_range"},
		{"growth window 1", "predicate", `{"kind":"output_growth_window","window":1}`, "trigger_spec.window=out_of_range"},
		{"window fractional", "predicate", `{"kind":"tool_calls_window","window":2.5}`, "trigger_spec.window=wrong_type"},
		{"window string", "predicate", `{"kind":"tool_calls_window","window":"3"}`, "trigger_spec.window=wrong_type"},
		{"factor zero", "predicate", `{"kind":"output_growth_window","factor":0}`, "trigger_spec.factor=out_of_range"},
		{"bad regex", "predicate", `{"kind":"regex_match_window","pattern":"(unclosed"}`, "trigger_spec.pattern=invalid_regex"},
		{"lookahead is not RE2", "predicate", `{"kind":"regex_match_window","pattern":"a(?=b)"}`, "trigger_spec.pattern=invalid_regex"},
		{"bad scope", "predicate", `{"kind":"text_regex_window","pattern":"x","scope":"everyone"}`, "trigger_spec.scope=invalid_value"},
		{"bad mode", "predicate", `{"kind":"tool_name_window","names":["a"],"mode":"some"}`, "trigger_spec.mode=invalid_value"},
		{"bad entity", "predicate", `{"kind":"entity_mention_window","entity":"email"}`, "trigger_spec.entity=invalid_value"},
		{"names entry not string", "predicate", `{"kind":"tool_name_window","names":["a",3]}`, "trigger_spec.names[1]=wrong_type"},
		{"prefix ratio negative", "predicate", `{"kind":"prefix_pressure","ratio":-1}`, "trigger_spec.ratio=out_of_range"},
		{"typo'd field", "predicate", `{"kind":"tool_calls_window","windw":3}`, "trigger_spec.windw=unknown_field"},
		{"scope on regex_match_window", "predicate", `{"kind":"regex_match_window","pattern":"x","scope":"user"}`, "trigger_spec.scope=unknown_field"},
		{"AND clause not object", "predicate", `{"kind":"AND","clauses":[3]}`, "trigger_spec.clauses[0]=not_object"},
		{"AND clauses not array", "predicate", `{"kind":"AND","clauses":{}}`, "trigger_spec.clauses=wrong_type"},
		{"spec not JSON", "predicate", `{`, "trigger_spec=invalid_json"},
		{"spec trailing data", "predicate", `{"kind":"attr","key":"k"} {}`, "trigger_spec=invalid_json"},
		{"spec is an array", "predicate", `[]`, "trigger_spec=not_object"},
		{"event name blank inside", "event", `{"name":"a b"}`, "trigger_spec.name=invalid_value"},
		{"event unknown field", "event", `{"name":"x","nmea":"y"}`, "trigger_spec.nmea=unknown_field"},
		{"interval zero", "interval", `{"every_n_ticks":0}`, "trigger_spec.every_n_ticks=out_of_range"},
		{"interval string", "interval", `{"every_n_ticks":"20"}`, "trigger_spec.every_n_ticks=wrong_type"},
		{"unknown trigger kind", "cron", `{}`, "trigger_kind=unknown_trigger_kind"},
		{"empty spec", "event", ``, "trigger_spec=required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateTriggerSpec(c.kind, c.spec); !has(err, c.want) {
				t.Fatalf("want %s, got %v", c.want, codes(err))
			}
		})
	}
}

func TestDefaultsAreAccepted(t *testing.T) {
	for _, s := range []string{
		`{"kind":"tool_calls_window"}`,
		`{"kind":"mail_unread_count"}`,
		`{"kind":"tool_name_window","names":"a, b"}`,
		`{"kind":"tool_calls_window","window":3.0}`,
		`{"kind":"tool_calls_window","op":"=="}`,
	} {
		if err := ValidateTriggerSpec("predicate", s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
}

func TestPredicateDepthIsBounded(t *testing.T) {
	spec := validLeaf["attr"]
	for i := 0; i < MaxPredicateDepth+5; i++ {
		spec = `{"kind":"AND","clauses":[` + spec + `]}`
	}
	if err := ValidateTriggerSpec("predicate", spec); !strings.Contains(err.Error(), CodeTooDeep) {
		t.Fatalf("got %v", err)
	}
}

func TestEventAndIntervalAccepted(t *testing.T) {
	if err := ValidateTriggerSpec("event", `{"name":"mail_received"}`); err != nil {
		t.Error(err)
	}
	if err := ValidateTriggerSpec("interval", `{"every_n_ticks":20}`); err != nil {
		t.Error(err)
	}
}

func TestWithKnownEvents(t *testing.T) {
	known := func(s string) bool { return s == "mail_received" }
	if err := ValidateTriggerSpec("event", `{"name":"mail_receivd"}`, WithKnownEvents(known)); !has(err, "trigger_spec.name=not_in_catalog") {
		t.Fatalf("got %v", err)
	}
	if err := ValidateTriggerSpec("event", `{"name":"mail_received"}`, WithKnownEvents(known)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTriggerSpec("event", `{"name":"anything_goes"}`); err != nil {
		t.Fatalf("default must be open: %v", err)
	}
}

func TestActionSpecPerKind(t *testing.T) {
	cases := []struct{ name, kind, spec, want string }{
		{"reminder ok", ActionInjectReminder, `{"body":"hi","urgency":"info"}`, ""},
		{"reminder novel urgency ok", ActionInjectReminder, `{"body":"hi","urgency":"critical"}`, ""},
		{"reminder extra field ok", ActionInjectReminder, `{"body":"hi","x":1}`, ""},
		{"reminder no body", ActionInjectReminder, `{"urgency":"info"}`, "action_spec.body=required"},
		{"reminder empty body", ActionInjectReminder, `{"body":" "}`, "action_spec.body=required"},
		{"reminder body number", ActionInjectReminder, `{"body":3}`, "action_spec.body=wrong_type"},
		{"reminder urgency shape", ActionInjectReminder, `{"body":"x","urgency":"Very Bad"}`, "action_spec.urgency=invalid_value"},
		{"halt ok", ActionHaltSession, `{"reason":"r"}`, ""},
		{"halt without reason ok", ActionHaltSession, `{}`, ""},
		{"halt reason type", ActionHaltSession, `{"reason":1}`, "action_spec.reason=wrong_type"},
		{"dispatch ok", ActionDispatchToAgent, `{"agent_slug":"planner","confidence":0.75,"reason":"r"}`, ""},
		{"dispatch minimal ok", ActionDispatchToAgent, `{"agent_slug":"planner"}`, ""},
		{"dispatch no slug", ActionDispatchToAgent, `{"confidence":0.5}`, "action_spec.agent_slug=required"},
		{"dispatch confidence high", ActionDispatchToAgent, `{"agent_slug":"a","confidence":1.5}`, "action_spec.confidence=out_of_range"},
		{"dispatch confidence type", ActionDispatchToAgent, `{"agent_slug":"a","confidence":"high"}`, "action_spec.confidence=wrong_type"},
		{"resume ok", ActionResumeLoopRun, `{"loop_run_id":"lr-1"}`, ""},
		{"resume no id", ActionResumeLoopRun, `{}`, "action_spec.loop_run_id=required"},
		{"empty spec", ActionHaltSession, ``, "action_spec=required"},
		{"not JSON", ActionHaltSession, `nope`, "action_spec=invalid_json"},
		{"array", ActionHaltSession, `[]`, "action_spec=not_object"},
		{"string", ActionHaltSession, `"x"`, "action_spec=not_object"},
		{"null", ActionHaltSession, `null`, "action_spec=not_object"},
		{"kind required", "", `{}`, "action_kind=required"},
		{"kind shape", "Halt Session", `{}`, "action_kind=invalid_value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateActionSpec(c.kind, c.spec)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected %v", err)
				}
				return
			}
			if !has(err, c.want) {
				t.Fatalf("want %s, got %v", c.want, codes(err))
			}
		})
	}
}

// TestUnevidencedActionKindsAreObjectOnly: by ruling, force_tool_choice,
// add_schedule and send_message (and any unknown kind) get no invented fields.
func TestUnevidencedActionKindsAreObjectOnly(t *testing.T) {
	for _, k := range []string{ActionForceToolChoice, ActionAddSchedule, ActionSendMessage, "future_kind"} {
		for _, ok := range []string{`{}`, `{"anything":[1,2,{"x":null}]}`, `{"tool_name":"x"}`} {
			if err := ValidateActionSpec(k, ok); err != nil {
				t.Errorf("%s %s: %v", k, ok, err)
			}
		}
		for _, bad := range []string{`[]`, `1`, `"s"`, `null`, `true`, `{`, ``} {
			if err := ValidateActionSpec(k, bad); err == nil {
				t.Errorf("%s %q: want error", k, bad)
			}
		}
	}
}

func TestWithKnownActionKindsAndTiers(t *testing.T) {
	kinds := func(s string) bool { return contains(DocumentedActionKinds(), s) }
	if err := ValidateActionSpec("future_kind", `{}`); err != nil {
		t.Fatalf("open by default: %v", err)
	}
	if err := ValidateActionSpec("future_kind", `{}`, WithKnownActionKinds(kinds)); !has(err, "action_kind=not_in_catalog") {
		t.Fatalf("got %v", err)
	}
	d := Definition{Name: "n", TriggerKind: "event", TriggerSpec: `{"name":"e"}`, ActionKind: "halt_session", ActionSpec: `{}`, ProvenanceTier: "plugin", Status: "active"}
	if errs := Validate(d); len(errs) != 0 {
		t.Fatalf("open by default: %v", errs)
	}
	errs := Validate(d,
		WithKnownProvenanceTiers(func(s string) bool { return s == "system" }),
		WithKnownStatuses(func(s string) bool { return s == "paused" }),
		WithKnownActionKinds(kinds))
	got := codes(errors.Join(errs...))
	sort.Strings(got)
	if strings.Join(got, ",") != "provenance_tier=not_in_catalog,status=not_in_catalog" {
		t.Fatalf("got %v", got)
	}
	if e := Validate(Definition{Name: "n", TriggerKind: "event", TriggerSpec: `{"name":"e"}`, ActionKind: "halt_session", ActionSpec: `{}`, ProvenanceTier: "Bad Tier"}); len(e) != 1 {
		t.Fatalf("tier shape: %v", e)
	}
}

func TestValidateDefinition(t *testing.T) {
	good := Definition{Name: "n", TriggerKind: "interval", TriggerSpec: `{"every_n_ticks":5}`, ActionKind: "halt_session", ActionSpec: `{"reason":"r"}`}
	if errs := Validate(good); len(errs) != 0 {
		t.Fatalf("got %v", errs)
	}
	neg := int64(-1)
	zero := int64(0)
	bad := Definition{RecurrenceOverrideSeconds: &neg}
	got := codes(errors.Join(Validate(bad)...))
	want := []string{"name=required", "trigger_kind=required", "action_kind=required", "action_spec=required", "recurrence_override_seconds=out_of_range"}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("missing %s in %v", w, got)
		}
	}
	good.RecurrenceOverrideSeconds = &zero
	if errs := Validate(good); len(errs) != 0 {
		t.Fatalf("explicit 0 must be valid: %v", errs)
	}
	// Validate must not mutate its input.
	c := good
	Validate(good)
	if c != good {
		t.Fatal("mutated")
	}
}

// TestRecurrenceOverrideNilVersusZero: nil and an explicit 0 must stay
// distinct through JSON in both directions.
func TestRecurrenceOverrideNilVersusZero(t *testing.T) {
	var absent, zero Definition
	if err := json.Unmarshal([]byte(`{"name":"a"}`), &absent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"name":"a","recurrence_override_seconds":0}`), &zero); err != nil {
		t.Fatal(err)
	}
	if absent.RecurrenceOverrideSeconds != nil {
		t.Fatal("absent decoded to non-nil")
	}
	if zero.RecurrenceOverrideSeconds == nil || *zero.RecurrenceOverrideSeconds != 0 {
		t.Fatal("explicit 0 lost")
	}
	b, _ := json.Marshal(zero)
	if !strings.Contains(string(b), `"recurrence_override_seconds":0`) {
		t.Fatalf("zero encoded as %s", b)
	}
	b, _ = json.Marshal(absent)
	if !strings.Contains(string(b), `"recurrence_override_seconds":null`) {
		t.Fatalf("nil encoded as %s", b)
	}
}

func TestDefinitionJSONTags(t *testing.T) {
	var d Definition
	in := `{"id":"i","agent_id":"a","class_tag":"c","name":"n","trigger_kind":"event","trigger_spec":"{}","action_kind":"k","action_spec":"{}","status":"s","priority":4,"opt_out_allowed":true,"provenance_tier":"p","recurrence_override_seconds":7,"workflow_run_id":"w","fired_count":9,"created_by":"x"}`
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != "i" || d.AgentID != "a" || d.ClassTag != "c" || d.Name != "n" || d.TriggerKind != "event" ||
		d.ActionKind != "k" || d.Status != "s" || d.Priority != 4 || !d.OptOutAllowed || d.ProvenanceTier != "p" ||
		*d.RecurrenceOverrideSeconds != 7 || d.WorkflowRunID != "w" {
		t.Fatalf("decoded %+v", d)
	}
}

type seedRow struct {
	Source      string `json:"source"`
	Name        string `json:"name"`
	TriggerKind string `json:"trigger_kind"`
	TriggerSpec string `json:"trigger_spec"`
	ActionKind  string `json:"action_kind"`
	ActionSpec  string `json:"action_spec"`
}

func loadSeeds(t *testing.T) []seedRow {
	t.Helper()
	b, err := os.ReadFile("testdata/nanite_seeds.json") //nolint:gosec // constant path
	if err != nil {
		t.Fatal(err)
	}
	var rows []seedRow
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestNaniteSeedActionSpecsAccepted: every real action_spec stored by
// Nanite's seeders (captured by running BaseSeeds and LoomPilotReflexSeeds
// at HEAD 6443d0fd and marshaling them as the seeder does) is accepted as-is.
func TestNaniteSeedActionSpecsAccepted(t *testing.T) {
	for _, s := range loadSeeds(t) {
		if err := ValidateActionSpec(s.ActionKind, s.ActionSpec, WithKnownActionKinds(func(k string) bool { return contains(DocumentedActionKinds(), k) })); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
	}
}

// TestNaniteSeedTriggersAgainstGoReflexesGrammar: this spec targets
// go-reflexes' grammar, which replaced Nanite's scope_tier and
// execution_pattern with attr. Seeds using those two kinds are rejected for
// exactly that reason; every other seed trigger is accepted.
func TestNaniteSeedTriggersAgainstGoReflexesGrammar(t *testing.T) {
	nanite := map[string]bool{"scope_tier": true, "execution_pattern": true}
	var sawRejected, sawAccepted bool
	for _, s := range loadSeeds(t) {
		err := ValidateTriggerSpec(s.TriggerKind, s.TriggerSpec)
		usesNaniteOnly := false
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if k, _ := x["kind"].(string); nanite[k] {
					usesNaniteOnly = true
				}
				if c, ok := x["clauses"].([]any); ok {
					for _, i := range c {
						walk(i)
					}
				}
			}
		}
		var root any
		_ = json.Unmarshal([]byte(s.TriggerSpec), &root)
		walk(root)
		if usesNaniteOnly {
			sawRejected = true
			if err == nil {
				t.Errorf("%s: uses a Nanite-only kind but was accepted", s.Name)
				continue
			}
			for _, c := range codes(err) {
				if !strings.HasSuffix(c, "="+CodeUnknownPredicateKind) {
					t.Errorf("%s: unexpected error %s", s.Name, c)
				}
			}
			continue
		}
		sawAccepted = true
		if err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
	}
	if !sawRejected || !sawAccepted {
		t.Fatal("fixture no longer exercises both outcomes")
	}
}

// TestGoReflexesVocabularyDrift compares PredicateKinds with the switch in a
// local go-reflexes checkout. It reads a file; it imports nothing, so there
// is no dependency edge. It skips when no checkout is present, so CI does not
// run it. Set GO_REFLEXES_DIR to point elsewhere.
func TestGoReflexesVocabularyDrift(t *testing.T) {
	dir := os.Getenv("GO_REFLEXES_DIR")
	if dir == "" {
		dir = "../go-reflexes"
	}
	src, err := os.ReadFile(dir + "/evaluator.go") //nolint:gosec // developer-supplied checkout path
	if err != nil {
		t.Skipf("no go-reflexes checkout at %s: %v", dir, err)
	}
	text := string(src)
	start := strings.Index(text, "func evalPredicateNode")
	end := strings.Index(text[start:], "default:")
	if start < 0 || end < 0 {
		t.Fatal("could not locate the predicate switch; update this test")
	}
	block := text[start : start+end]
	re := regexp.MustCompile(`(?m)^\tcase ("[^\n]*):$`)
	var upstream []string
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
			upstream = append(upstream, q[1])
		}
	}
	sort.Strings(upstream)
	ours := PredicateKinds()
	if strings.Join(upstream, ",") != strings.Join(ours, ",") {
		t.Fatalf("predicate vocabulary drifted\n go-reflexes: %v\n this spec:  %v", upstream, ours)
	}
}

// TestStdlibOnly guards the "no go-reflexes, no other lib" boundary.
func TestStdlibOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./...").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if !strings.HasPrefix(p, "github.com/hollis-labs/reflex-definition-spec") {
			t.Errorf("non-stdlib dependency %s", p)
		}
	}
}

func TestErrorString(t *testing.T) {
	e := newErr("a.b", CodeRequired, "a.b is required")
	if e.Error() != "a.b: a.b is required [required]" {
		t.Fatal(e.Error())
	}
}

func TestUnknownTriggerKindReportedOnce(t *testing.T) {
	errs := Validate(Definition{Name: "n", TriggerKind: "cron", TriggerSpec: `{}`, ActionKind: "halt_session", ActionSpec: `{}`})
	if len(errs) != 1 {
		t.Fatalf("got %v", errs)
	}
}
