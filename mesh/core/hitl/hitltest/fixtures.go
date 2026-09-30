package hitltest

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

//go:embed testdata/valid/*.json testdata/invalid/*.json
var fixtureFS embed.FS

// Fixture is one document to validate against one schema definition.
type Fixture struct {
	// Name is unique across valid and invalid fixtures.
	Name string
	// Def is the schema definition the document is checked against.
	Def string
	// Valid reports whether the document must validate.
	Valid bool
	// RejectsBecause, for an invalid fixture, is the JSON Pointer of the
	// instance location that validation must fail at ("" is the root object,
	// for example for a missing required member). Empty for valid fixtures.
	RejectsBecause string
	// Doc is the document itself.
	Doc []byte
}

type fixtureFile struct {
	Def            string          `json:"def"`
	Name           string          `json:"name"`
	RejectsBecause *string         `json:"rejects_because"`
	Doc            json.RawMessage `json:"doc"`
}

// Fixtures returns every embedded fixture, valid first, sorted by name.
func Fixtures() []Fixture {
	var out []Fixture
	for _, dir := range []string{"valid", "invalid"} {
		entries, err := fs.ReadDir(fixtureFS, path.Join("testdata", dir))
		if err != nil {
			panic("hitltest: " + err.Error())
		}
		for _, e := range entries {
			b, err := fixtureFS.ReadFile(path.Join("testdata", dir, e.Name()))
			if err != nil {
				panic("hitltest: " + err.Error())
			}
			var f fixtureFile
			if err := json.Unmarshal(b, &f); err != nil {
				panic(fmt.Sprintf("hitltest: fixture %s/%s: %v", dir, e.Name(), err))
			}
			fx := Fixture{Name: f.Name, Def: f.Def, Valid: dir == "valid", Doc: f.Doc}
			if f.RejectsBecause != nil {
				fx.RejectsBecause = *f.RejectsBecause
			}
			if !fx.Valid && f.RejectsBecause == nil {
				panic(fmt.Sprintf("hitltest: invalid fixture %s records no rejects_because", e.Name()))
			}
			out = append(out, fx)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Valid != out[j].Valid {
			return out[i].Valid
		}
		return out[i].Name < out[j].Name
	})
	return out
}
