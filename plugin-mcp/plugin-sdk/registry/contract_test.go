package registry

// Contract fixtures: the Go half of the go/TS wire-contract check.
//
// registry/testdata/contract/protocol-N/*.json are read by this test AND by
// ts/packages/plugin-registry/test/contract-fixtures.test.js. Each file holds
// one raw wire response plus what each view must make of it. Files are FROZEN
// per released protocol: once protocol N ships, its directory is append-only.
// A later change that breaks a frozen file — even one made to Go and TS
// together — must bump Protocol, which is exactly the host/loader version-skew
// case a same-commit fixture cannot otherwise see. Only the directory for the
// current Protocol is executed; older directories stay as history.
//
// Neither half counts fixtures; both fail when they find none (examining
// nothing is not a pass).
//
// DELETION CONDITION (this is a bridge between two hand-authored views): delete
// when either view is generated from the other, or both consume one schema.
//
// Known asymmetry, encoded on purpose in unknown-plugin.json and not fixed: a
// contribution naming a plugin the response does not describe fails Validate
// here (ErrUnknownPlugin), while the TS loader declares it, leaves it
// unresolved and does not refuse it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type contractFixture struct {
	Description string          `json:"description"`
	Response    json.RawMessage `json:"response"`
	Go          struct {
		Validate  string `json:"validate"`
		Roundtrip *bool  `json:"roundtrip"`
	} `json:"go"`
}

func classify(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrProtocol):
		return "ErrProtocol"
	case errors.Is(err, ErrInvalidContribution):
		return "ErrInvalidContribution"
	case errors.Is(err, ErrUnknownPlugin):
		return "ErrUnknownPlugin"
	}
	return "unclassified: " + err.Error()
}

// jsonTags returns the json field names of a struct type.
func jsonTags(t reflect.Type) []string {
	var tags []string
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			tags = append(tags, name)
		}
	}
	return tags
}

// wireKeys records which structural keys a response uses, by level. Keys inside
// a contribution's opaque meta are deliberately not collected.
func wireKeys(resp map[string]any, seen map[string]map[string]bool) {
	mark := func(level string, m map[string]any) {
		for k := range m {
			seen[level][k] = true
		}
	}
	mark("response", resp)
	plugins, _ := resp["plugins"].(map[string]any)
	for _, p := range plugins {
		pm, _ := p.(map[string]any)
		mark("plugin", pm)
		if rt, ok := pm["runtime"].(map[string]any); ok {
			mark("runtime", rt)
		}
	}
	contributions, _ := resp["contributions"].(map[string]any)
	for _, byKey := range contributions {
		bm, _ := byKey.(map[string]any)
		for _, c := range bm {
			cm, _ := c.(map[string]any)
			mark("contribution", cm)
		}
	}
}

func TestContractFixtures(t *testing.T) {
	dir := filepath.Join("testdata", "contract", fmt.Sprintf("protocol-%d", Protocol))
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no contract fixtures in %s: examined nothing, which is not a pass", dir)
	}

	seen := map[string]map[string]bool{"response": {}, "plugin": {}, "runtime": {}, "contribution": {}}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var fx contractFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("fixture is not valid: %v", err)
			}
			var resp Response
			if err := json.Unmarshal(fx.Response, &resp); err != nil {
				t.Fatalf("response does not unmarshal into Response: %v", err)
			}
			if got := classify(resp.Validate()); got != fx.Go.Validate {
				t.Errorf("Validate() = %s, fixture says %s (%s)", got, fx.Go.Validate, fx.Description)
			}
			if fx.Go.Validate != "ok" {
				return
			}

			var generic map[string]any
			if err := json.Unmarshal(fx.Response, &generic); err != nil {
				t.Fatal(err)
			}
			wireKeys(generic, seen)

			if fx.Go.Roundtrip != nil && !*fx.Go.Roundtrip {
				return
			}
			out, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			var again map[string]any
			if err := json.Unmarshal(out, &again); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(generic, again) {
				t.Errorf("round trip changed the response:\n in: %s\nout: %s", fx.Response, out)
			}
		})
	}

	// No wire key without a fixture: every json tag on the wire types must
	// appear in at least one accepted fixture. Derived by reflection, so a field
	// added to a struct is a failing test until a fixture carries it.
	for level, typ := range map[string]reflect.Type{
		"response":     reflect.TypeOf(Response{}),
		"plugin":       reflect.TypeOf(Plugin{}),
		"runtime":      reflect.TypeOf(Runtime{}),
		"contribution": reflect.TypeOf(Contribution{}),
	} {
		for _, tag := range jsonTags(typ) {
			if !seen[level][tag] {
				t.Errorf("wire key %q (%s) appears in no accepted fixture in %s", tag, level, dir)
			}
		}
	}
}
