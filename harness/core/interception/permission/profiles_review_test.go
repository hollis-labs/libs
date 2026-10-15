package permission

import (
	"errors"
	"reflect"
	"testing"
)

func TestMissingProfileReferenceIsDistinct(t *testing.T) {
	_, err := BindProfile("", Ceiling{})
	var pe *ProfileError
	if !errors.As(err, &pe) || pe.Code != "missing_permission_profile" {
		t.Fatal(err)
	}
}
func TestCeilingExpressesDenyRequirement(t *testing.T) {
	arg := reflect.TypeOf(BindProfile).In(1)
	if arg.Kind() != reflect.Struct {
		t.Fatal("ceiling cannot express deny-rule enforcement")
	}
	field, ok := arg.FieldByName("RequiresDenyEnforcement")
	if !ok || field.Type.Kind() != reflect.Bool {
		t.Fatal("missing explicit deny enforcement requirement")
	}
}
func TestYoloRefusesRequiredDenyEnforcement(t *testing.T) {
	for _, name := range []string{"yolo", "catalog-auto"} {
		_, err := BindProfile(name, Ceiling{Modes: []Mode{ModeYolo}, RequiresDenyEnforcement: true})
		var pe *ProfileError
		if !errors.As(err, &pe) || pe.Code != CodeDenyEnforcement {
			t.Fatal(err)
		}
	}
	binding, err := BindProfile("default", Ceiling{Modes: []Mode{ModeDefault}, RequiresDenyEnforcement: true})
	if err != nil || binding.SkipsDenyRules {
		t.Fatal(binding, err)
	}
}
func TestExportedProfileRefusalCodes(t *testing.T) {
	for _, c := range []struct {
		name    string
		ceiling Ceiling
		code    string
	}{
		{"", Ceiling{}, CodeMissingProfile}, {"unknown", Ceiling{}, CodeUnknownProfile},
		{"default", Ceiling{}, CodePermissionCeiling},
		{"yolo", Ceiling{Modes: []Mode{ModeYolo}, RequiresDenyEnforcement: true}, CodeDenyEnforcement},
		{"catalog-auto", Ceiling{Modes: []Mode{ModeYolo}, RequiresDenyEnforcement: true}, CodeDenyEnforcement},
		{"catalog-auto", Ceiling{Modes: []Mode{ModePlan}}, CodePermissionCeiling},
	} {
		_, err := BindProfile(c.name, c.ceiling)
		var pe *ProfileError
		if !errors.As(err, &pe) || pe.Code != c.code {
			t.Fatal(err)
		}
	}
}
