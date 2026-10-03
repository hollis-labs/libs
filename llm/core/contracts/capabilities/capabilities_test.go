package capabilities

import (
	"reflect"
	"testing"
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name      string
		supported Set
		required  Set
		want      Set
	}{
		{"missing from empty", nil, Set{Identity, MCP}, Set{Identity, MCP}},
		{"missing none", Set{Identity, MCP}, Set{MCP}, nil},
		{"nothing required", Set{Identity}, nil, nil},
		{"partial overlap", Set{Identity, Memory}, Set{Memory, Sandbox, Hooks}, Set{Sandbox, Hooks}},
		{"duplicates collapse", nil, Set{Hooks, Hooks}, Set{Hooks}},
		{"open vocabulary", Set{"vendor-x"}, Set{"vendor-x", "vendor-y"}, Set{"vendor-y"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(tc.supported, tc.required)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Check(%v, %v) = %v, want %v", tc.supported, tc.required, got, tc.want)
			}
		})
	}
}

// Check must never grow a way to be told "force": overriding is the caller's
// decision. The signature is exactly (Set, Set) -> Set.
func TestCheckTakesNoForceFlag(t *testing.T) {
	ft := reflect.TypeOf(Check)
	setType := reflect.TypeOf(Set(nil))
	if ft.NumIn() != 2 || ft.In(0) != setType || ft.In(1) != setType || ft.NumOut() != 1 || ft.Out(0) != setType {
		t.Errorf("Check signature is %v, want func(Set, Set) Set", ft)
	}
}

func TestKnown(t *testing.T) {
	for _, n := range []Name{Identity, Memory, Resume, LongRunning, Skills, Subagents, MCP, Sandbox, Hooks} {
		if !Known(n) {
			t.Errorf("Known(%q) = false", n)
		}
	}
	if Known("nonsense") || Known("") {
		t.Error("unknown names must not be Known")
	}
}

func TestLevelValid(t *testing.T) {
	if !LevelRequires.Valid() || !LevelUses.Valid() || Level("").Valid() || Level("must").Valid() {
		t.Error("Level.Valid is wrong")
	}
}

func TestSetHas(t *testing.T) {
	s := Set{Identity, MCP}
	if !s.Has(MCP) || s.Has(Hooks) || Set(nil).Has(Identity) {
		t.Error("Set.Has is wrong")
	}
}
