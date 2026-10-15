package keymerge_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
)

func mergeTOML(t *testing.T, desired, existing string, owned ...keymerge.KeyPath) keymerge.TOMLMerge {
	t.Helper()
	got, err := keymerge.MergeTOML([]byte(desired), []byte(existing), owned)
	if err != nil {
		t.Fatalf("MergeTOML(%q, %q): unexpected error %v", desired, existing, err)
	}
	return got
}

func tomlNotes(got keymerge.TOMLMerge) []string {
	var out []string
	for _, n := range got.Notes {
		out = append(out, string(n.Code)+" "+strings.Join(n.Path, "/"))
	}
	return out
}

// TestMergeTOMLOwnedKeyRule states the ownership rule case by case with every
// declared leaf owned. The expected documents are what the TOML encoder writes:
// keys sorted, plain values before tables, comments gone, strings in the
// encoder's quoting.
func TestMergeTOMLOwnedKeyRule(t *testing.T) {
	for _, tc := range []struct {
		name     string
		desired  string
		existing string
		owned    []keymerge.KeyPath
		want     string
	}{{
		name:     "declared keys win, undeclared keys stand, tables compose",
		desired:  "a = 1\n[t]\nk = 1\n",
		existing: "b = 2\n[t]\nz = 9\n[u]\nq = 'x'\n",
		owned:    []keymerge.KeyPath{p("a"), p("t", "k")},
		want:     "a = 1\nb = 2\n\n[t]\nk = 1\nz = 9\n\n[u]\nq = 'x'\n",
	}, {
		name:     "a table over a scalar is the declared table",
		desired:  "[t]\nk = 1\n[t.s]\nm = 2\n",
		existing: "t = \"scalar\"\n",
		owned:    []keymerge.KeyPath{p("t", "k"), p("t", "s", "m")},
		want:     "[t]\nk = 1\n\n[t.s]\nm = 2\n",
	}, {
		name:     "a scalar over a table is the declared scalar",
		desired:  "t = 1\n",
		existing: "[t]\nk = 1\n",
		owned:    []keymerge.KeyPath{p("t")},
		want:     "t = 1\n",
	}, {
		name:     "an array is replaced whole, comments are dropped, strings are re-quoted",
		desired:  "x = [1, 2]\n",
		existing: "x = [3]\n# comment\ny = \"dq\"\n",
		owned:    []keymerge.KeyPath{p("x")},
		want:     "x = [1, 2]\ny = 'dq'\n",
	}, {
		name:     "an array of tables is one leaf",
		desired:  "[[srv]]\nname = 'a'\n",
		existing: "[[srv]]\nname = 'b'\n[[srv]]\nname = 'c'\n",
		owned:    []keymerge.KeyPath{p("srv")},
		want:     "[[srv]]\nname = 'a'\n",
	}, {
		name:     "values are re-encoded, not copied",
		desired:  "d = 1979-05-27T07:32:00Z\nld = 1979-05-27\nlt = 07:32:00\nf = 1.50\ni = 0xff\ns = '''multi\nline'''\n",
		existing: "n = nan\n",
		owned:    []keymerge.KeyPath{p("d"), p("ld"), p("lt"), p("f"), p("i"), p("s")},
		want:     "d = 1979-05-27T07:32:00Z\nf = 1.5\ni = 255\nld = 1979-05-27\nlt = 07:32:00\nn = nan\ns = \"multi\\nline\"\n",
	}, {
		name:     "an empty table found stays",
		desired:  "k = 1\n",
		existing: "[t]\n[t.u]\n",
		owned:    []keymerge.KeyPath{p("k")},
		want:     "k = 1\n\n[t]\n[t.u]\n",
	}, {
		name:     "inline tables compose like tables",
		desired:  "inline = {a = 1, b = {c = 2}}\n",
		existing: "inline = {z = 9}\n",
		owned:    []keymerge.KeyPath{p("inline", "a"), p("inline", "b", "c")},
		want:     "[inline]\na = 1\nz = 9\n\n[inline.b]\nc = 2\n",
	}, {
		name:     "an empty key and a dotted key are single components",
		desired:  "\"\" = 1\n\"a.b\" = 2\n",
		existing: "\"a.b\" = 0\n",
		owned:    []keymerge.KeyPath{p(""), p("a.b")},
		want:     "'' = 1\n'a.b' = 2\n",
	}, {
		name:     "an array may mix values and tables",
		desired:  "arr = [1, 'a', {x = 1}]\n",
		existing: "",
		owned:    []keymerge.KeyPath{p("arr")},
		want:     "arr = [1, 'a', {x = 1}]\n",
	}, {
		name:     "an empty table declared over a table leaves its keys",
		desired:  "[t]\n",
		existing: "[t]\nk = 1\n",
		owned:    []keymerge.KeyPath{p("t")},
		want:     "[t]\nk = 1\n",
	}, {
		name:     "an empty table declared over a scalar is the declared table",
		desired:  "[t]\n",
		existing: "t = 1\n",
		owned:    []keymerge.KeyPath{p("t")},
		want:     "[t]\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeTOML(t, tc.desired, tc.existing, tc.owned...)
			if string(got.Document) != tc.want {
				t.Errorf("desired %q\nexisting %q\ngave\n\t%q\nwant\n\t%q", tc.desired, tc.existing, got.Document, tc.want)
			}
		})
	}
}

// TestMergeTOMLEmptyDocumentsAreTables: a document with nothing in it, or only
// comments, is the empty table, so an empty existing document merges to the
// desired document re-encoded. (JSON differs: there, empty existing returns the
// desired bytes untouched.)
func TestMergeTOMLEmptyDocumentsAreTables(t *testing.T) {
	owned := []keymerge.KeyPath{p("a"), p("b")}
	for _, existing := range []string{"", " \n", "# only a comment\n"} {
		got := mergeTOML(t, "b = 2\na = 1\n", existing, owned...)
		if want := "a = 1\nb = 2\n"; string(got.Document) != want {
			t.Errorf("existing %q: gave %q, want %q", existing, got.Document, want)
		}
	}
	got := mergeTOML(t, "", "a = 1\n")
	if want := "a = 1\n"; string(got.Document) != want {
		t.Errorf("empty desired: gave %q, want %q", got.Document, want)
	}
	got = mergeTOML(t, "", "")
	if len(got.Document) != 0 {
		t.Errorf("two empty documents: gave %q, want nothing", got.Document)
	}
}

// TestMergeTOMLErrorsAreTypedAndHideTheDocument: a document that does not parse
// is a typed error that carries a code, a reason and a position, and none of the
// document's text.
func TestMergeTOMLErrorsAreTypedAndHideTheDocument(t *testing.T) {
	owned := []keymerge.KeyPath{p("a")}
	const echoed = "fixture-document-text"
	for _, tc := range []struct {
		name     string
		desired  string
		existing string
		code     keymerge.Code
	}{
		{"an existing document that is not TOML", "a = 1\n", "model = [\n" + `broken = "` + echoed + "\n", keymerge.CodeExistingTOMLInvalid},
		{"a desired document that is not TOML", "a = [\n" + echoed, "a = 1\n", keymerge.CodeDesiredTOMLInvalid},
		{"both invalid: the desired document is reported, as it is read first", "a = [\n", "b = [\n", keymerge.CodeDesiredTOMLInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := keymerge.MergeTOML([]byte(tc.desired), []byte(tc.existing), owned)
			if !errors.Is(err, &keymerge.Error{Code: tc.code}) {
				t.Fatalf("error = %v, want code %q", err, tc.code)
			}
			var typed *keymerge.Error
			if !errors.As(err, &typed) {
				t.Fatalf("error %T is not a *keymerge.Error", err)
			}
			if typed.Reason != keymerge.ReasonNotTOML || typed.Line < 1 || typed.Column < 1 {
				t.Errorf("error = %#v, want reason not_toml with a 1-based position", typed)
			}
			if strings.Contains(err.Error(), echoed) || strings.Contains(err.Error(), "broken") {
				t.Errorf("the error text echoes the document: %q", err.Error())
			}
			if errors.Is(err, &keymerge.Error{Code: keymerge.CodeInvalidOwnedPaths}) {
				t.Error("a parse failure matched the owned-paths code")
			}
		})
	}
}

// TestMergeTOMLOwnership: a declared key the caller does not own is never
// overwritten and never added.
func TestMergeTOMLOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		desired  string
		existing string
		owned    []keymerge.KeyPath
		want     string
		notes    []string
	}{{
		name:     "a declared key that is not owned keeps the found value",
		desired:  "model = 'slot'\nmode = 'auto'\n",
		existing: "model = 'operator'\nmode = 'plan'\n",
		owned:    []keymerge.KeyPath{p("mode")},
		want:     "mode = 'auto'\nmodel = 'operator'\n",
		notes:    []string{"unowned_kept model"},
	}, {
		name:     "equal values produce no note",
		desired:  "model = 'same'\nmode = 'auto'\n",
		existing: "model = 'same'\nmode = 'plan'\n",
		owned:    []keymerge.KeyPath{p("mode")},
		want:     "mode = 'auto'\nmodel = 'same'\n",
	}, {
		name:     "a declared key that is not owned and absent is skipped",
		desired:  "model = 'slot'\nmode = 'auto'\n",
		existing: "other = 1\n",
		owned:    []keymerge.KeyPath{p("mode")},
		want:     "mode = 'auto'\nother = 1\n",
		notes:    []string{"unowned_skipped model"},
	}, {
		name:     "an absent owned table is written with its owned leaves only",
		desired:  "[srv]\ncommand = 'run'\nargs = ['a']\nslot = 'keep'\n[srv.deep]\nx = 1\ny = 2\n",
		existing: "other = 1\n",
		owned:    []keymerge.KeyPath{p("srv", "command"), p("srv", "args"), p("srv", "deep", "y")},
		want:     "other = 1\n\n[srv]\nargs = ['a']\ncommand = 'run'\n\n[srv.deep]\ny = 2\n",
		notes:    []string{"unowned_skipped srv/deep/x", "unowned_skipped srv/slot"},
	}, {
		name:     "a found table composes and an unowned declared key inside it stands",
		desired:  "[srv]\ncommand = 'run'\nslot = 'declared'\n",
		existing: "[srv]\ncommand = 'old'\nslot = 'operator'\nextra = true\n",
		owned:    []keymerge.KeyPath{p("srv", "command")},
		want:     "[srv]\ncommand = 'run'\nextra = true\nslot = 'operator'\n",
		notes:    []string{"unowned_kept srv/slot"},
	}, {
		name:     "an owned table over a found scalar is written pruned, with notes",
		desired:  "[srv]\ncommand = 'run'\nslot = 'declared'\n",
		existing: "srv = 'scalar'\n",
		owned:    []keymerge.KeyPath{p("srv", "command")},
		want:     "[srv]\ncommand = 'run'\n",
		notes:    []string{"type_conflict srv", "unowned_skipped srv/slot"},
	}, {
		name:     "a found table over an owned scalar is replaced, with a note",
		desired:  "t = 1\n",
		existing: "[t]\nk = 1\n",
		owned:    []keymerge.KeyPath{p("t")},
		want:     "t = 1\n",
		notes:    []string{"type_conflict t"},
	}, {
		name:     "an unowned table over a found scalar leaves the scalar",
		desired:  "mode = 'auto'\n[srv]\nslot = 'declared'\n",
		existing: "srv = 'scalar'\n",
		owned:    []keymerge.KeyPath{p("mode")},
		want:     "mode = 'auto'\nsrv = 'scalar'\n",
		notes:    []string{"unowned_kept srv"},
	}, {
		name:     "an owned path with no declared value changes nothing",
		desired:  "mode = 'auto'\n",
		existing: "mode = 'plan'\ngone = 'operator'\n",
		owned:    []keymerge.KeyPath{p("mode"), p("gone")},
		want:     "gone = 'operator'\nmode = 'auto'\n",
	}, {
		name:     "an owned path that only passes through a declared leaf owns nothing there",
		desired:  "a = 5\n",
		existing: "[a]\nb = 1\n",
		owned:    []keymerge.KeyPath{p("a", "b")},
		want:     "[a]\nb = 1\n",
		notes:    []string{"unowned_kept a"},
	}, {
		name:     "nothing owned writes nothing",
		desired:  "a = 1\n[b]\nc = 2\n",
		existing: "z = 0\n",
		owned:    nil,
		want:     "z = 0\n",
		notes:    []string{"unowned_skipped a", "unowned_skipped b"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeTOML(t, tc.desired, tc.existing, tc.owned...)
			if string(got.Document) != tc.want {
				t.Errorf("gave\n\t%q\nwant\n\t%q", got.Document, tc.want)
			}
			if n := tomlNotes(got); !sameStrings(n, tc.notes) {
				t.Errorf("notes = %v, want %v", n, tc.notes)
			}
		})
	}
}

// TestMergeTOMLNotesAreOrderedByKeyPath: a decoded TOML tree has no document
// order, so notes are ordered by key path, whatever the document orders say.
func TestMergeTOMLNotesAreOrderedByKeyPath(t *testing.T) {
	got := mergeTOML(t, "c = 1\nb = 1\n[a]\nx = 1\n", "c = 2\nb = 2\n[a]\nx = 2\n")
	want := []string{"unowned_kept a", "unowned_kept b", "unowned_kept c"}
	if n := tomlNotes(got); !sameStrings(n, want) {
		t.Errorf("notes = %v, want %v", n, want)
	}
	got = mergeTOML(t, "[a]\ny = 1\nx = 1\n[a.z]\nk = 1\n", "[a]\ny = 2\nx = 2\n[a.z]\nk = 2\n", p("a", "y"))
	want = []string{"unowned_kept a/x", "unowned_kept a/z"}
	if n := tomlNotes(got); !sameStrings(n, want) {
		t.Errorf("notes = %v, want %v", n, want)
	}
}

// TestMergeTOMLRefusesUnusableOwnedPaths: the same loud errors as MergeJSON, in
// the same order: an empty path, then the desired document, then a path naming a
// non-empty table, then the document found.
func TestMergeTOMLRefusesUnusableOwnedPaths(t *testing.T) {
	want := &keymerge.Error{Code: keymerge.CodeInvalidOwnedPaths}
	for _, tc := range []struct {
		name     string
		desired  string
		existing string
		owned    []keymerge.KeyPath
		reason   keymerge.Reason
	}{
		{"a path with no components", "a = 1\n", "a = 2\n", []keymerge.KeyPath{{}}, keymerge.ReasonEmptyPath},
		{"a path naming a non-empty table", "[t]\nk = 1\n", "[t]\nk = 2\n", []keymerge.KeyPath{p("t")}, keymerge.ReasonNotLeaf},
		{"a path naming a non-empty table, found unreadable", "[t]\nk = 1\n", "x = [\n", []keymerge.KeyPath{p("t")}, keymerge.ReasonNotLeaf},
		{"a bad path outranks an unreadable desired document", "a = [\n", "", []keymerge.KeyPath{{}}, keymerge.ReasonEmptyPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := keymerge.MergeTOML([]byte(tc.desired), []byte(tc.existing), tc.owned)
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			var typed *keymerge.Error
			if !errors.As(err, &typed) || typed.Reason != tc.reason {
				t.Errorf("error = %#v, want reason %q", typed, tc.reason)
			}
		})
	}
	for _, desired := range []string{"b = 1\n", "a = 1\n", "[a]\n", "a = [1]\n", "[[a]]\nb = 1\n"} {
		if _, err := keymerge.MergeTOML([]byte(desired), nil, []keymerge.KeyPath{p("a"), p("a", "b")}); err != nil {
			t.Errorf("desired %q: unexpected error %v", desired, err)
		}
	}
	// An unreadable desired document is reported as that, not as a bad path.
	_, err := keymerge.MergeTOML([]byte("a = [\n"), nil, []keymerge.KeyPath{p("a")})
	if !errors.Is(err, &keymerge.Error{Code: keymerge.CodeDesiredTOMLInvalid}) {
		t.Errorf("error = %v, want desired_toml_invalid", err)
	}
}

// TestMergeTOMLIsIdempotentDeterministicAndDetached.
func TestMergeTOMLIsIdempotentDeterministicAndDetached(t *testing.T) {
	desired := []byte("model = 'opus'\n[env]\nA = '1'\n[slot]\nx = 1\n")
	existing := []byte("tui = 'x'\nmodel = 'haiku'\n[env]\nB = '2'\n")
	owned := []keymerge.KeyPath{p("model"), p("env", "A")}
	d0, e0 := bytes.Clone(desired), bytes.Clone(existing)

	once, err := keymerge.MergeTOML(desired, existing, owned)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(desired, d0) || !bytes.Equal(existing, e0) {
		t.Fatal("an input was modified")
	}
	twice, err := keymerge.MergeTOML(desired, once.Document, owned)
	if err != nil || !bytes.Equal(once.Document, twice.Document) {
		t.Errorf("merging twice moved the document:\n\tonce  %q\n\ttwice %q (%v)", once.Document, twice.Document, err)
	}
	for i := 0; i < 20; i++ {
		again, _ := keymerge.MergeTOML(desired, existing, owned)
		if !bytes.Equal(again.Document, once.Document) || !sameStrings(tomlNotes(again), tomlNotes(once)) {
			t.Fatalf("call %d answered differently", i)
		}
	}
	for i := range once.Document {
		once.Document[i] = 'X'
	}
	again, _ := keymerge.MergeTOML(desired, existing, owned)
	if bytes.Contains(again.Document, []byte("X")) {
		t.Fatal("the output shares memory with a later call")
	}
}

// TestNormalizeTOML: the document as the encoder would write it, and the bytes
// back, in a copy, when it does not parse.
func TestNormalizeTOML(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"comments and quoting", "b = \"x\" # c\na = 1\n", "a = 1\nb = 'x'\n", true},
		{"already normal", "a = 1\n", "a = 1\n", true},
		{"empty", "", "", true},
		{"only a comment", "# nothing\n", "", true},
		{"not TOML", "a = [\n", "a = [\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.in)
			got, ok := keymerge.NormalizeTOML(in)
			if string(got) != tc.want || ok != tc.ok {
				t.Errorf("NormalizeTOML(%q) = %q, %v, want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
			if len(got) > 0 {
				got[0] = 'X'
				if string(in) != tc.in {
					t.Errorf("the result of NormalizeTOML(%q) aliases the input", tc.in)
				}
			}
		})
	}
}

// TestMergeTOMLNotePathsOfSiblingsAreIndependent: notes for sibling keys deep in
// a document each carry their own path.
func TestMergeTOMLNotePathsOfSiblingsAreIndependent(t *testing.T) {
	got := mergeTOML(t,
		"[a.b.c]\nx = 1\ny = 2\nw = 3\n",
		"[a.b.c]\nx = 9\ny = 8\nw = 7\n",
		p("a", "b", "c", "w"))
	if n := tomlNotes(got); !sameStrings(n, []string{"unowned_kept a/b/c/x", "unowned_kept a/b/c/y"}) {
		t.Errorf("notes = %v, want the two siblings with their own paths", n)
	}
	got.Notes[0].Path[0] = "changed"
	if got.Notes[1].Path[0] != "a" {
		t.Error("two notes share a path")
	}
}
