package conformance

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	reflexspec "github.com/hollis-labs/reflex-definition-spec"
)

// FixtureFS holds the fixture tree, rooted at Root.
//
//go:embed testdata
var FixtureFS embed.FS

// Root is the directory inside FixtureFS that holds the cases.
const Root = "testdata"

// WantError is one expected validation failure.
type WantError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// Options names the catalogs a case injects. A nil list means "no catalog";
// an empty non-nil list is a catalog that knows nothing.
type Options struct {
	KnownActionKinds     []string `json:"known_action_kinds"`
	KnownProvenanceTiers []string `json:"known_provenance_tiers"`
	KnownStatuses        []string `json:"known_statuses"`
	KnownEvents          []string `json:"known_events"`
}

// Want is the expected outcome of one case.
type Want struct {
	Valid   bool        `json:"valid"`
	Errors  []WantError `json:"errors"`
	Reason  string      `json:"reason"`
	Options Options     `json:"options"`
}

// Case is one fixture.
type Case struct {
	// Dir is the case directory, for example testdata/invalid/kind-missing.
	Dir   string
	Input reflexspec.Definition
	Want  Want
}

// Load reads every case under root in fsys (nil means FixtureFS). It rejects
// a case missing either file, an input or want that does not decode strictly,
// a valid case that lists errors, and an invalid case with no errors or no
// recorded reason.
func Load(fsys fs.FS, root string) ([]Case, error) {
	if fsys == nil {
		fsys = FixtureFS
	}
	var cases []Case
	for _, group := range []string{"valid", "invalid"} {
		entries, err := fs.ReadDir(fsys, path.Join(root, group))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				return nil, fmt.Errorf("%s/%s: unexpected file", group, e.Name())
			}
			dir := path.Join(root, group, e.Name())
			c, err := loadCase(fsys, dir)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", dir, err)
			}
			if c.Want.Valid != (group == "valid") {
				return nil, fmt.Errorf("%s: want.valid=%v contradicts its %s/ directory", dir, c.Want.Valid, group)
			}
			cases = append(cases, c)
		}
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Dir < cases[j].Dir })
	return cases, nil
}

func strictDecode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func loadCase(fsys fs.FS, dir string) (Case, error) {
	c := Case{Dir: dir}
	in, err := fs.ReadFile(fsys, path.Join(dir, "input.json"))
	if err != nil {
		return c, err
	}
	w, err := fs.ReadFile(fsys, path.Join(dir, "want.json"))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(in, &c.Input); err != nil {
		return c, fmt.Errorf("input.json: %w", err)
	}
	if err := strictDecode(w, &c.Want); err != nil {
		return c, fmt.Errorf("want.json: %w", err)
	}
	switch {
	case c.Want.Valid && len(c.Want.Errors) > 0:
		return c, fmt.Errorf("valid case lists errors")
	case !c.Want.Valid && len(c.Want.Errors) == 0:
		return c, fmt.Errorf("invalid case lists no errors")
	case !c.Want.Valid && strings.TrimSpace(c.Want.Reason) == "":
		return c, fmt.Errorf("invalid case records no reason")
	}
	return c, nil
}

func catalog(list []string) func(string) bool {
	if list == nil {
		return nil
	}
	return func(s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
}

// Run validates the case with reflexspec.Validate and returns nil when the
// outcome matches Want: the same set of (field, code) pairs, or none for a
// valid case.
func Run(c Case) error {
	o := c.Want.Options
	var opts []reflexspec.ValidateOption
	if f := catalog(o.KnownActionKinds); f != nil {
		opts = append(opts, reflexspec.WithKnownActionKinds(f))
	}
	if f := catalog(o.KnownProvenanceTiers); f != nil {
		opts = append(opts, reflexspec.WithKnownProvenanceTiers(f))
	}
	if f := catalog(o.KnownStatuses); f != nil {
		opts = append(opts, reflexspec.WithKnownStatuses(f))
	}
	if f := catalog(o.KnownEvents); f != nil {
		opts = append(opts, reflexspec.WithKnownEvents(f))
	}
	var got []string
	for _, err := range reflexspec.Validate(c.Input, opts...) {
		var e *reflexspec.Error
		ok := errors.As(err, &e)
		if !ok {
			return fmt.Errorf("validate returned %T, want *reflexspec.Error", err)
		}
		got = append(got, e.Field+" "+e.Code)
	}
	var want []string
	for _, e := range c.Want.Errors {
		want = append(want, e.Field+" "+e.Code)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		return fmt.Errorf("got errors %q, want %q", got, want)
	}
	return nil
}
