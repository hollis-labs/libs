package plan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

func TestACPOnlyRefusals(t *testing.T) {
	for _, p := range []runtimes.ID{runtimes.Copilot, runtimes.Pi} {
		_, err := Find(key(p, Instructions))
		code(t, err, "unsupported_runtime")
		k := key(p, Instructions)
		k.Mode = runtimes.ModeACPStdio
		_, err = Find(k)
		code(t, err, "unsupported_runtime")
	}
}
func TestEffectLocatorsEmpty(t *testing.T) {
	for _, r := range Table() {
		if r.Form == Link || r.Form == RuntimeBinding {
			if !reflect.DeepEqual(r.Locator, Locator{}) {
				t.Errorf("effect carries launch locator: %s %s %s", r.Provider, r.Field, r.Mode)
			}
			mutated := r.clone()
			mutated.Locator.Argv = []string{"--add-dir", "{P}"}
			if Validate([]Row{mutated}) == nil {
				t.Error("effect launch argv accepted")
			}
		}
	}
}
func TestRequirementMustBeExplicit(t *testing.T) {
	_, err := Resolve(Request{Key: key(runtimes.Claude, Instructions)})
	code(t, err, "invalid_request")
}
func TestPostureIsBoundMode(t *testing.T) {
	field, ok := reflect.TypeOf(Request{Requirement: Optional}).FieldByName("Posture")
	if !ok || field.Type != reflect.TypeOf(permission.ModeDefault) {
		t.Fatal("request must carry bound permission.Mode")
	}
}
func TestLayoutVocabularyIndependent(t *testing.T) {
	expected := reflect.TypeOf(Row{}).PkgPath()
	for _, v := range []any{Row{}.Root, Row{}.Variant} {
		if reflect.TypeOf(v).PkgPath() != expected {
			t.Fatal("plan vocabulary depends on legacy layout package")
		}
	}
}
func TestSupportUsesSuppliedTable(t *testing.T) {
	_, err := FindIn(nil, key(runtimes.Claude, Instructions))
	code(t, err, "unsupported_provider")
}
func TestCredentialPathCannotHideBehindOtherField(t *testing.T) {
	for _, p := range []string{"auth.json", ".claude/.credentials.json", ".gemini/oauth_creds.json"} {
		r, _ := Find(key(runtimes.Codex, Settings))
		r.Path = p
		r.Field = Settings
		if Validate([]Row{r}) == nil {
			t.Errorf("credential path accepted as file: %s", p)
		}
	}
}
func TestEvidenceIsPublicReference(t *testing.T) {
	for _, r := range Table() {
		if strings.HasPrefix(r.Evidence.Reference, "ADR ") {
			t.Errorf("non-public evidence reference for %s/%s", r.Provider, r.Field)
		}
	}
}
func TestUncomposedPathCollision(t *testing.T) {
	a, _ := Find(key(runtimes.Claude, Commands))
	a.Composition = ""
	b := a.clone()
	b.Field = Prompts
	if Validate([]Row{a, b}) == nil {
		t.Fatal("uncomposed command/prompt path collision accepted")
	}
}
func TestAliasCanonicalization(t *testing.T) {
	if NormalizeProvider(" AGY ") != runtimes.Antigravity || NormalizeProvider(" Claude-Code ") != runtimes.Claude {
		t.Fatal("aliases not trimmed and lower-cased")
	}
}
func TestOpenCodeRunToken(t *testing.T) {
	r, _ := Find(key(runtimes.OpenCode, Instructions))
	if r.Locator.Argv[0] != "run" {
		t.Fatal("OpenCode per-turn binding lacks run token")
	}
}
func TestSupportSelectorChangesDriveLookup(t *testing.T) {
	saved := supportedShapes
	defer func() { supportedShapes = saved }()
	supportedShapes = nil
	_, err := Find(key(runtimes.Claude, Instructions))
	code(t, err, "unsupported_layer")
	supportedShapes = []shapeSupport{{runtimes.Claude, Boot, runtimes.ModeJSONRPCStdio, ""}}
	k := key(runtimes.Claude, Instructions)
	k.Mode = runtimes.ModeJSONRPCStdio
	if _, err := Find(k); err != nil {
		t.Fatal("authored shape not used:", err)
	}
}
func TestExplicitCompositionAndExpandedCollisions(t *testing.T) {
	a, _ := Find(key(runtimes.Claude, Commands))
	b := a.clone()
	b.Field = Prompts
	if err := Validate([]Row{a, b}); err != nil {
		t.Fatal("declared shared serializer refused:", err)
	}
	b.Composition = "different"
	code(t, Validate([]Row{a, b}), "path_collision")
}

func TestExportedCloneDetached(t *testing.T) {
	in := Row{Locator: Locator{Argv: []string{"original"}, Env: map[string]Root{"ROOT": RootBoot}}, Evidence: Evidence{Observations: []string{"original"}}, Posture: &PostureReference{Mapper: "original"}}
	out := in.Clone()
	out.Locator.Argv[0] = "changed"
	out.Locator.Env["ROOT"] = RootHome
	out.Evidence.Observations[0] = "changed"
	out.Posture.Mapper = "changed"
	if in.Locator.Argv[0] != "original" || in.Locator.Env["ROOT"] != RootBoot || in.Evidence.Observations[0] != "original" || in.Posture.Mapper != "original" {
		t.Fatal("Clone aliases")
	}
}
func TestCredentialPredicateComponentsAndCase(t *testing.T) {
	for _, p := range []string{"auth.json", ".Credentials.json/key", "nested/OAUTH_CREDS.JSON", "Auth.JSON/key", "a/.credentials.json/b"} {
		if !IsCredentialDestination(p) {
			t.Fatal(p)
		}
	}
	if IsCredentialDestination("authentication.json") {
		t.Fatal("false credential")
	}
}
