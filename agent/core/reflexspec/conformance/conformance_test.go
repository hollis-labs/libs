package conformance

import (
	"strings"
	"testing"
	"testing/fstest"

	reflexspec "github.com/hollis-labs/reflex-definition-spec"
)

// TestFixtures runs every embedded case against the validator: valid cases
// report nothing, invalid cases report exactly the recorded (field, code)
// pairs.
func TestFixtures(t *testing.T) {
	cases, err := Load(nil, Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no fixtures loaded")
	}
	for _, c := range cases {
		t.Run(strings.TrimPrefix(c.Dir, Root+"/"), func(t *testing.T) {
			if err := Run(c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestEveryPredicateKindHasAValidFixture ties the fixtures to the grammar so
// a new kind cannot ship without one.
func TestEveryPredicateKindHasAValidFixture(t *testing.T) {
	cases, err := Load(nil, Root)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if c.Want.Valid && c.Input.TriggerKind == "predicate" {
			for _, k := range reflexspec.PredicateKinds() {
				if strings.Contains(c.Input.TriggerSpec, `"kind":"`+k+`"`) {
					seen[k] = true
				}
			}
		}
	}
	for _, k := range reflexspec.PredicateKinds() {
		if !seen[k] {
			t.Errorf("no valid fixture uses predicate kind %q", k)
		}
	}
}

func TestLoadRejectsMalformedCases(t *testing.T) {
	mk := func(want string) fstest.MapFS {
		return fstest.MapFS{
			"r/valid/x/input.json":   {Data: []byte(`{}`)},
			"r/valid/x/want.json":    {Data: []byte(`{"valid":true}`)},
			"r/invalid/y/input.json": {Data: []byte(`{}`)},
			"r/invalid/y/want.json":  {Data: []byte(want)},
		}
	}
	for name, want := range map[string]string{
		"no errors":        `{"valid":false,"reason":"r"}`,
		"no reason":        `{"valid":false,"errors":[{"field":"a","code":"b"}]}`,
		"contradiction":    `{"valid":true}`,
		"unknown property": `{"valid":false,"errors":[{"field":"a","code":"b"}],"reason":"r","extra":1}`,
	} {
		if _, err := Load(mk(want), "r"); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := Load(mk(`{"valid":false,"errors":[{"field":"a","code":"b"}],"reason":"r"}`), "r"); err != nil {
		t.Errorf("well-formed: %v", err)
	}
}

func TestRunReportsMismatch(t *testing.T) {
	c := Case{Input: reflexspec.Definition{}, Want: Want{Valid: true}}
	if Run(c) == nil {
		t.Fatal("an empty definition must not match a valid case")
	}
	c = Case{Input: reflexspec.Definition{Name: "n", TriggerKind: "event", TriggerSpec: `{"name":"e"}`, ActionKind: "halt_session", ActionSpec: `{}`},
		Want: Want{Errors: []WantError{{"name", "required"}}, Reason: "r"}}
	if Run(c) == nil {
		t.Fatal("a valid definition must not match an invalid case")
	}
}
