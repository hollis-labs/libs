package keymerge_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
	"github.com/pelletier/go-toml/v2"
)

// The fuzz targets run as ordinary tests over their seed corpora in the normal
// gate. They assert what must hold for any bytes at all: no panic, a typed
// failure or a valid document, and a result that merges to itself.

// ownedFromSeed reads owned paths from a fuzz string: one path per line,
// components separated by "/". The empty string is no paths.
func ownedFromSeed(s string) []keymerge.KeyPath {
	if s == "" {
		return nil
	}
	var out []keymerge.KeyPath
	for _, line := range strings.Split(s, "\n") {
		out = append(out, keymerge.KeyPath(strings.Split(line, "/")))
	}
	return out
}

func FuzzMergeJSON(f *testing.F) {
	for _, seed := range [][3]string{
		{`{"a":1}`, `{"a":2,"b":3}`, "a"},
		{`{"a":{"b":1,"c":2}}`, `{"a":{"b":0,"d":4}}`, "a/b"},
		{`{"a":{"b":1}}`, `{"a":"x"}`, "a/b"},
		{`{"a":{}}`, `{"a":{"x":1}}`, "a"},
		{`{"a":1,"a":2}`, `{}`, ""},
		{`{"a":1}`, `{"a":1,"a":2}`, "a"},
		{`{"a":1}`, `{"a":{"x":1,"x":2}}`, "a"},
		{`{"a":{"x":1,"x":2}}`, `{"a":{"y":1}}`, "a/x"},
		{`{"a":1}`, `{"a":1} trailing`, "a"},
		{`{"a":1}`, ``, "a"},
		{`{"a":1}`, `[1]`, "a"},
		{`{"a":1}`, `{"a":2}`, "a"},
		{`{"a":{"b":1}}`, `{}`, "a"},
		{`{"a":1}`, `{"a":1.50,"b":1E+2,"c":"<&>"}`, "a\n\n"},
		{"{\"\":{\"a.b\":1}}", `{"":{"a.b":0}}`, "/a.b"},
		{`[`, `{}`, "a"},
		{"\xff", "\xfe", "\xff"},
	} {
		f.Add([]byte(seed[0]), []byte(seed[1]), seed[2])
	}
	f.Fuzz(func(t *testing.T, desired, existing []byte, owned string) {
		paths := ownedFromSeed(owned)
		d0, e0 := bytes.Clone(desired), bytes.Clone(existing)
		got, err := keymerge.MergeJSON(desired, existing, paths)
		if !bytes.Equal(desired, d0) || !bytes.Equal(existing, e0) {
			t.Fatal("an input was modified")
		}
		if err != nil {
			if !errors.Is(err, &keymerge.Error{Code: keymerge.CodeInvalidOwnedPaths}) {
				t.Fatalf("untyped error %v", err)
			}
			return
		}
		switch got.Outcome {
		case keymerge.OutcomeMerged:
			if got.Reason != "" || len(got.Document) < 2 || got.Document[0] != '{' || got.Document[len(got.Document)-1] != '\n' || !json.Valid(got.Document) {
				t.Fatalf("a merged document that is not one laid-out JSON object: %q", got.Document)
			}
			again, err := keymerge.MergeJSON(desired, got.Document, paths)
			if err != nil || again.Outcome != keymerge.OutcomeMerged || !bytes.Equal(again.Document, got.Document) {
				t.Fatalf("merging the result moved it:\nonce  %q\ntwice %q (%v, %s)", got.Document, again.Document, err, again.Outcome)
			}
		case keymerge.OutcomeExistingUnreadable, keymerge.OutcomeDesiredUnreadable:
			if got.Reason == "" || !bytes.Equal(got.Document, desired) || len(got.Notes) != 0 {
				t.Fatalf("an unreadable outcome %s/%s with document %q and notes %v", got.Outcome, got.Reason, got.Document, got.Notes)
			}
		default:
			t.Fatalf("unknown outcome %q", got.Outcome)
		}
		cmp, err := keymerge.CompareJSON(desired, existing, paths)
		if err != nil || cmp.Outcome != got.Outcome || cmp.Reason != got.Reason || cmp.WriteLen != len(got.Document) {
			t.Fatalf("CompareJSON = %+v, %v, disagrees with the merge", cmp, err)
		}
	})
}

func FuzzMergeTOML(f *testing.F) {
	for _, seed := range [][3]string{
		{"a = 1\n", "a = 2\nb = 3\n", "a"},
		{"[t]\nk = 1\n", "[t]\nk = 0\nz = 9\n", "t/k"},
		{"[t]\nk = 1\n", "t = 'x'\n", "t/k"},
		{"[t]\n", "[t]\nk = 1\n", "t"},
		{"[[s]]\nn = 'a'\n", "[[s]]\nn = 'b'\n", "s"},
		{"a = [\n", "", ""},
		{"a = 1\n", "b = [\n", "a"},
		{"a = 1\n", "", "a\n\n"},
		{"d = 1979-05-27T07:32:00Z\nf = nan\n", "g = inf\n", "d\nf"},
		{"x = {a = 1, b = {c = 2}}\n", "x = {z = 9}\n", "x/a\nx/b/c"},
		{"\"\" = 1\n\"a.b\" = 2\n", "", "\n/a.b"},
		{"\xff", "\xfe", "\xff"},
	} {
		f.Add([]byte(seed[0]), []byte(seed[1]), seed[2])
	}
	f.Fuzz(func(t *testing.T, desired, existing []byte, owned string) {
		paths := ownedFromSeed(owned)
		d0, e0 := bytes.Clone(desired), bytes.Clone(existing)
		got, err := keymerge.MergeTOML(desired, existing, paths)
		if !bytes.Equal(desired, d0) || !bytes.Equal(existing, e0) {
			t.Fatal("an input was modified")
		}
		if err != nil {
			var typed *keymerge.Error
			if !errors.As(err, &typed) {
				t.Fatalf("untyped error %v", err)
			}
			switch typed.Code {
			case keymerge.CodeExistingTOMLInvalid, keymerge.CodeDesiredTOMLInvalid, keymerge.CodeInvalidOwnedPaths:
			default:
				t.Fatalf("unknown code %q", typed.Code)
			}
			return
		}
		var tree map[string]any
		if err := toml.Unmarshal(got.Document, &tree); err != nil {
			t.Fatalf("the merged document does not parse: %v\n%q", err, got.Document)
		}
		again, err := keymerge.MergeTOML(desired, got.Document, paths)
		if err != nil || !bytes.Equal(again.Document, got.Document) {
			t.Fatalf("merging the result moved it:\nonce  %q\ntwice %q (%v)", got.Document, again.Document, err)
		}
		if cmp, err := keymerge.CompareTOML(desired, existing, paths); err != nil || cmp.Outcome != keymerge.OutcomeMerged || cmp.WriteLen != len(got.Document) {
			t.Fatalf("CompareTOML = %+v, %v, disagrees with the merge", cmp, err)
		}
	})
}

func FuzzNormalizeTOML(f *testing.F) {
	for _, seed := range []string{
		"", "# only a comment\n", "a = 1\n", "b = \"x\" # c\na = 1\n", "a = [\n", "[t]\n[t.u]\n",
		"x = {a = 1}\n", "[[s]]\nn = 1\n[[s]]\nn = 2\n", "d = 1979-05-27\nt = 07:32:00.5\n",
		"s = '''multi\nline'''\n", "f = 1e400\n", "\xff",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		r0 := bytes.Clone(raw)
		out, ok := keymerge.NormalizeTOML(raw)
		if !bytes.Equal(raw, r0) {
			t.Fatal("the input was modified")
		}
		if !ok {
			if !bytes.Equal(out, raw) {
				t.Fatalf("an unparseable document came back changed: %q", out)
			}
			return
		}
		again, ok := keymerge.NormalizeTOML(out)
		if !ok || !bytes.Equal(again, out) {
			t.Fatalf("the normal form is not a fixed point:\nonce  %q\ntwice %q", out, again)
		}
	})
}
