package hitltest_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/hollis-labs/go-hitl/hitltest"
	"github.com/hollis-labs/go-hitl/schema"
)

func TestFixturesValidateAgainstTheirDefinition(t *testing.T) {
	for _, f := range hitltest.Fixtures() {
		if !f.Valid {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			v, err := schema.NewValidator(f.Def)
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Validate(f.Doc); err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
		})
	}
}

// A document rejected for the wrong reason is a vacuous pass, so every invalid
// fixture must fail AT the JSON Pointer it records.
func TestInvalidFixturesAreRejectedAtTheirRecordedPointer(t *testing.T) {
	for _, f := range hitltest.Fixtures() {
		if f.Valid {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			v, err := schema.NewValidator(f.Def)
			if err != nil {
				t.Fatal(err)
			}
			err = v.Validate(f.Doc)
			if err == nil {
				t.Fatal("invalid fixture was accepted")
			}
			var verr *schema.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("want *schema.ValidationError, got %v", err)
			}
			if !slices.Contains(verr.Locations, f.RejectsBecause) {
				t.Fatalf("rejected at %q, want %q\n%v", verr.Locations, f.RejectsBecause, verr.Detail)
			}
		})
	}
}

func TestEveryFixtureNamesAKnownDefinitionAndNamesAreUnique(t *testing.T) {
	defs := schema.Defs()
	seen := map[string]bool{}
	for _, f := range hitltest.Fixtures() {
		if !slices.Contains(defs, f.Def) {
			t.Errorf("%s: unknown def %q", f.Name, f.Def)
		}
		if seen[f.Name] {
			t.Errorf("duplicate fixture name %q", f.Name)
		}
		seen[f.Name] = true
	}
}
