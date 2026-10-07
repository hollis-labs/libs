package capability

import (
	"errors"
	"reflect"
	"testing"
)

func TestIntersectionNarrowing(t *testing.T) {
	requested := Scope{map[string][]string{"targets": {"b", "a", "a"}, "effects": {"read", "write"}}, map[string]int64{"bytes": 100, "rows": 10}}
	supported := Scope{map[string][]string{"targets": {"a", "b"}, "effects": {"read", "write"}}, map[string]int64{"bytes": 80, "rows": 10}}
	approved := Scope{map[string][]string{"targets": {"a"}, "effects": {"read"}}, map[string]int64{"bytes": 60, "rows": 5}}
	policy := Scope{map[string][]string{"targets": {"a"}, "effects": {"read"}}, map[string]int64{"bytes": 40}}
	got, err := Intersect(MCPReach, requested, supported, approved, policy)
	if err != nil {
		t.Fatal(err)
	}
	want := Scope{map[string][]string{"targets": {"a"}, "effects": {"read"}}, map[string]int64{"bytes": 40, "rows": 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope = %#v", got)
	}
	for _, upper := range []Scope{requested, supported, approved, policy} {
		if err := CheckNarrowing(MCPReach, upper, got); err != nil {
			t.Fatal(err)
		}
	}
	got.Allowlists["targets"][0] = "other"
	got.Limits["bytes"] = 1000
	if requested.Allowlists["targets"][0] != "b" || policy.Limits["bytes"] != 40 {
		t.Fatal("intersection aliases input")
	}
}
func TestMissingScopeDeniesAndWideningNamesCapability(t *testing.T) {
	initial := Scope{map[string][]string{"targets": {"one"}}, map[string]int64{"bytes": 5}}
	denied, err := Intersect(ReadonlyQuery, initial, Scope{}, initial, initial)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allows("targets", "one") || denied.Within("missing", 0) || denied.Within("bytes", 1) {
		t.Fatal("missing constraint authorized")
	}
	for _, next := range []Scope{
		{Allowlists: map[string][]string{"targets": {"two"}}},
		{Limits: map[string]int64{"bytes": 6}},
		{Limits: map[string]int64{"new": 1}},
	} {
		err := CheckNarrowing(ReadonlyQuery, initial, next)
		var e *Error
		if !errors.As(err, &e) || e.Code != ScopeDenied || e.Capability != ReadonlyQuery || e.EffectState != NotStarted {
			t.Fatalf("widening: %v", err)
		}
	}
}
func TestMalformedScopes(t *testing.T) {
	for _, s := range []Scope{
		{Allowlists: map[string][]string{"targets": {"*"}}},
		{Allowlists: map[string][]string{"targets": {""}}},
		{Limits: map[string]int64{"bytes": -1}},
	} {
		if _, err := Intersect(MCPReach, s, s, s, s); err == nil {
			t.Fatalf("accepted %#v", s)
		}
	}
}

func TestScopePortableLimitBoundary(t *testing.T) {
	for _, value := range []int64{int64(MaxSafeInteger), int64(MaxSafeInteger) + 1, -1} {
		s := Scope{Limits: map[string]int64{"bytes": value}}
		err := s.Validate(ReadonlyQuery)
		if value == int64(MaxSafeInteger) && err != nil {
			t.Fatal("maximum safe limit rejected")
		}
		if value != int64(MaxSafeInteger) && err == nil {
			t.Fatal("nonportable limit accepted")
		}
	}
}
