package keymerge_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
)

// p builds a key path from its components.
func p(parts ...string) keymerge.KeyPath { return keymerge.KeyPath(parts) }

func mergeJSON(t *testing.T, desired, existing string, owned ...keymerge.KeyPath) keymerge.JSONMerge {
	t.Helper()
	got, err := keymerge.MergeJSON([]byte(desired), []byte(existing), owned)
	if err != nil {
		t.Fatalf("MergeJSON(%q, %q): unexpected error %v", desired, existing, err)
	}
	return got
}

func notesOf(got keymerge.JSONMerge) []string {
	var out []string
	for _, n := range got.Notes {
		out = append(out, string(n.Code)+" "+strings.Join(n.Path, "/"))
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMergeJSONOwnedKeyRule states the ownership rule case by case, with every
// declared leaf owned: a key the desired document declares carries the desired
// value, and a key it does not carries the bytes that were found.
func TestMergeJSONOwnedKeyRule(t *testing.T) {
	for _, tc := range []struct {
		name     string
		desired  string
		existing string
		owned    []keymerge.KeyPath
		want     string
	}{{
		name:     "a key never declared is kept",
		desired:  `{"fixture-model":"a"}`,
		existing: `{"fixture-model":"b","fixture-tui":"c"}`,
		owned:    []keymerge.KeyPath{p("fixture-model")},
		want:     "{\n  \"fixture-model\": \"a\",\n  \"fixture-tui\": \"c\"\n}\n",
	}, {
		name:     "a declared key is overwritten",
		desired:  `{"fixture-model":"a"}`,
		existing: `{"fixture-model":"b"}`,
		owned:    []keymerge.KeyPath{p("fixture-model")},
		want:     "{\n  \"fixture-model\": \"a\"\n}\n",
	}, {
		name:     "a declared key the document lacks is added after the found keys",
		desired:  `{"fixture-model":"a"}`,
		existing: `{"fixture-tui":"c"}`,
		owned:    []keymerge.KeyPath{p("fixture-model")},
		want:     "{\n  \"fixture-tui\": \"c\",\n  \"fixture-model\": \"a\"\n}\n",
	}, {
		name:     "an undeclared key one level down is kept",
		desired:  `{"fixture-perms":{"mode":"auto"}}`,
		existing: `{"fixture-perms":{"allow":["x"],"mode":"plan"}}`,
		owned:    []keymerge.KeyPath{p("fixture-perms", "mode")},
		want:     "{\n  \"fixture-perms\": {\n    \"allow\": [\n      \"x\"\n    ],\n    \"mode\": \"auto\"\n  }\n}\n",
	}, {
		name:     "an array at a declared key is replaced whole",
		desired:  `{"fixture-hooks":["a"]}`,
		existing: `{"fixture-hooks":["b","c"]}`,
		owned:    []keymerge.KeyPath{p("fixture-hooks")},
		want:     "{\n  \"fixture-hooks\": [\n    \"a\"\n  ]\n}\n",
	}, {
		name:     "an object replacing a scalar at a declared key is the desired one",
		desired:  `{"fixture-perms":{"mode":"auto"}}`,
		existing: `{"fixture-perms":"auto"}`,
		owned:    []keymerge.KeyPath{p("fixture-perms", "mode")},
		want:     "{\n  \"fixture-perms\": {\n    \"mode\": \"auto\"\n  }\n}\n",
	}, {
		name:     "a scalar replacing an object at a declared key is the desired one",
		desired:  `{"fixture-perms":"auto"}`,
		existing: `{"fixture-perms":{"mode":"plan","allow":[]}}`,
		owned:    []keymerge.KeyPath{p("fixture-perms")},
		want:     "{\n  \"fixture-perms\": \"auto\"\n}\n",
	}, {
		name:     "null is a declared value like any other",
		desired:  `{"fixture-a":null}`,
		existing: `{"fixture-a":{"b":1}}`,
		owned:    []keymerge.KeyPath{p("fixture-a")},
		want:     "{\n  \"fixture-a\": null\n}\n",
	}, {
		name:     "an empty object declared over an object leaves the found members",
		desired:  `{"fixture-env":{}}`,
		existing: `{"fixture-env":{"A":"1"}}`,
		owned:    []keymerge.KeyPath{p("fixture-env")},
		want:     "{\n  \"fixture-env\": {\n    \"A\": \"1\"\n  }\n}\n",
	}, {
		name:     "an empty object declared over a scalar is the desired one",
		desired:  `{"fixture-env":{}}`,
		existing: `{"fixture-env":"x"}`,
		owned:    []keymerge.KeyPath{p("fixture-env")},
		want:     "{\n  \"fixture-env\": {}\n}\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeJSON(t, tc.desired, tc.existing, tc.owned...)
			if got.Outcome != keymerge.OutcomeMerged || got.Reason != "" {
				t.Fatalf("outcome = %q/%q, want merged", got.Outcome, got.Reason)
			}
			if string(got.Document) != tc.want {
				t.Errorf("desired %s\nexisting %s\ngave\n\t%q\nwant\n\t%q", tc.desired, tc.existing, got.Document, tc.want)
			}
		})
	}
}

// TestMergeJSONUnreadableDocumentsReturnTheDesiredBytes: where a document is
// not one readable object the desired document comes back untouched, as the
// installer overwrites there, and the outcome names why.
func TestMergeJSONUnreadableDocumentsReturnTheDesiredBytes(t *testing.T) {
	const desired = "{\"fixture-model\":\"a\"}" // deliberately compact, no newline
	owned := []keymerge.KeyPath{p("fixture-model")}
	for _, tc := range []struct {
		name     string
		existing string
		reason   keymerge.Reason
	}{
		{"nothing on disk", "", keymerge.ReasonEmpty},
		{"only white space", " \n\t\r ", keymerge.ReasonEmpty},
		{"not JSON", "not a settings document\n", keymerge.ReasonNotJSON},
		{"a truncated object", `{"fixture-tui":"x"`, keymerge.ReasonNotJSON},
		{"a trailing comma", `{"fixture-tui":"x",}`, keymerge.ReasonNotJSON},
		{"a malformed array is not JSON before it is not an object", `["a",`, keymerge.ReasonNotJSON},
		{"an array", `["fixture-model"]`, keymerge.ReasonNotObject},
		{"a string", `"fixture-model"`, keymerge.ReasonNotObject},
		{"null", `null`, keymerge.ReasonNotObject},
		{"a number then junk is not an object before it has trailing content", `12 x`, keymerge.ReasonNotObject},
		{"trailing content", `{"fixture-tui":"x"} and then some`, keymerge.ReasonTrailingContent},
		{"two objects", `{"a":1}{"b":2}`, keymerge.ReasonTrailingContent},
		{"a duplicate key", `{"fixture-tui":"x","fixture-tui":"y"}`, keymerge.ReasonDuplicateKey},
		{"two spellings of one key", `{"a":1,"\u0061":2}`, keymerge.ReasonDuplicateKey},
		{"trailing content outranks a duplicate key", `{"a":1,"a":2} x`, keymerge.ReasonTrailingContent},
		{"a malformed object outranks a duplicate key", `{"a":1,"a":2,`, keymerge.ReasonNotJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeJSON(t, desired, tc.existing, owned...)
			if got.Outcome != keymerge.OutcomeExistingUnreadable || got.Reason != tc.reason {
				t.Fatalf("outcome = %q/%q, want existing_unreadable/%q", got.Outcome, got.Reason, tc.reason)
			}
			if string(got.Document) != desired {
				t.Errorf("document = %q, want the desired bytes %q untouched", got.Document, desired)
			}
			if len(got.Notes) != 0 {
				t.Errorf("notes = %v, want none", notesOf(got))
			}
		})
	}
}

// TestMergeJSONUnreadableDesiredIsAnOutcome: a desired document that is not one
// readable object is an outcome with the bytes unchanged, never an error.
func TestMergeJSONUnreadableDesiredIsAnOutcome(t *testing.T) {
	const existing = `{"fixture-tui":"x"}`
	for _, tc := range []struct {
		desired string
		reason  keymerge.Reason
	}{
		{"", keymerge.ReasonEmpty},
		{"nope", keymerge.ReasonNotJSON},
		{`[1]`, keymerge.ReasonNotObject},
		{`{"a":1} x`, keymerge.ReasonTrailingContent},
		{`{"a":1,"a":2}`, keymerge.ReasonDuplicateKey},
	} {
		got := mergeJSON(t, tc.desired, existing, p("a"))
		if got.Outcome != keymerge.OutcomeDesiredUnreadable || got.Reason != tc.reason {
			t.Errorf("desired %q: outcome = %q/%q, want desired_unreadable/%q", tc.desired, got.Outcome, got.Reason, tc.reason)
		}
		if string(got.Document) != tc.desired {
			t.Errorf("desired %q: document = %q, want the desired bytes", tc.desired, got.Document)
		}
	}
}

// TestMergeJSONAtRestDocumentIsReturnedByteForByte pins the exact statement:
// a document whose declared values already agree merges to its own layout, so
// a document at rest comes back byte for byte and one laid out some other way
// comes back in the layout this package writes.
func TestMergeJSONAtRestDocumentIsReturnedByteForByte(t *testing.T) {
	owned := []keymerge.KeyPath{p("fixture-model"), p("fixture-perms", "mode")}
	const desired = `{"fixture-model":"opus","fixture-perms":{"mode":"auto"}}`
	atRest := "{\n  \"fixture-model\": \"opus\",\n  \"fixture-perms\": {\n    \"mode\": \"auto\"\n  }\n}\n"
	for _, existing := range []string{
		atRest,
		`{"fixture-model":"opus","fixture-perms":{"mode":"auto"}}`,
		"{\n\t\"fixture-model\": \"opus\",\n\t\"fixture-perms\": {\"mode\": \"auto\"}\n}",
	} {
		got := mergeJSON(t, desired, existing, owned...)
		if string(got.Document) != atRest {
			t.Errorf("existing %q merged to\n\t%q\nwant the at-rest layout\n\t%q", existing, got.Document, atRest)
		}
	}
}

// TestMergeJSONKeepsFoundOrder: the order is the order found, with keys only
// the desired document declares after it, whatever the operator's keys do.
func TestMergeJSONKeepsFoundOrder(t *testing.T) {
	owned := []keymerge.KeyPath{p("fixture-perms", "mode"), p("fixture-z")}
	const desired = `{"fixture-perms":{"mode":"auto"},"fixture-z":1}`
	for _, tc := range []struct {
		name     string
		existing string
		want     string
	}{{
		name:     "an operator key before the declared one",
		existing: `{"fixture_operator":true,"fixture-perms":{"mode":"plan"},"fixture-z":0}`,
		want:     "{\n  \"fixture_operator\": true,\n  \"fixture-perms\": {\n    \"mode\": \"auto\"\n  },\n  \"fixture-z\": 1\n}\n",
	}, {
		name:     "an operator key between declared ones",
		existing: `{"fixture-perms":{"mode":"plan"},"fixture_operator":true,"fixture-z":0}`,
		want:     "{\n  \"fixture-perms\": {\n    \"mode\": \"auto\"\n  },\n  \"fixture_operator\": true,\n  \"fixture-z\": 1\n}\n",
	}, {
		name:     "an operator key after the declared ones",
		existing: `{"fixture-perms":{"mode":"plan"},"fixture-z":0,"fixture_operator":true}`,
		want:     "{\n  \"fixture-perms\": {\n    \"mode\": \"auto\"\n  },\n  \"fixture-z\": 1,\n  \"fixture_operator\": true\n}\n",
	}, {
		name:     "declared keys found in the opposite order stay where they were found",
		existing: `{"fixture-z":0,"fixture-perms":{"mode":"plan"}}`,
		want:     "{\n  \"fixture-z\": 1,\n  \"fixture-perms\": {\n    \"mode\": \"auto\"\n  }\n}\n",
	}, {
		name:     "operator keys inside an owned object keep their place",
		existing: `{"fixture-perms":{"b":2,"mode":"plan","a":1},"fixture-z":0}`,
		want:     "{\n  \"fixture-perms\": {\n    \"b\": 2,\n    \"mode\": \"auto\",\n    \"a\": 1\n  },\n  \"fixture-z\": 1\n}\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeJSON(t, desired, tc.existing, owned...)
			if string(got.Document) != tc.want {
				t.Errorf("gave\n\t%q\nwant\n\t%q", got.Document, tc.want)
			}
		})
	}
}

// TestMergeJSONDoesNotRespellWhatItCopies: keys and values are copied as raw
// bytes from whichever document they came from, so nothing is re-encoded.
func TestMergeJSONDoesNotRespellWhatItCopies(t *testing.T) {
	got := mergeJSON(t,
		`{"fixture-model":"opus"}`,
		`{"a < b":"c && d > e","fixture-n":[1E+2,0.10,-0,1e400,123456789012345678901234567890],"fixture-s":"tab\there \u00e9 \ud83d"}`,
		p("fixture-model"))
	doc := string(got.Document)
	for _, want := range []string{`"a < b"`, `"c && d > e"`, `1E+2`, `0.10`, `-0`, `1e400`, `123456789012345678901234567890`, `\t`, `\u00e9`, `\ud83d`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the merged document lost %s:\n%s", want, doc)
		}
	}
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(doc, escaped) {
			t.Errorf("the merged document holds %s: something re-encoded what it was handed:\n%s", escaped, doc)
		}
	}
	// The desired side is copied raw too.
	got = mergeJSON(t, `{"fixture-x":"<&>","fixture-n":1.50}`, `{}`, p("fixture-x"), p("fixture-n"))
	if want := "{\n  \"fixture-x\": \"<&>\",\n  \"fixture-n\": 1.50\n}\n"; string(got.Document) != want {
		t.Errorf("gave %q, want %q", got.Document, want)
	}
}

// TestMergeJSONMatchesKeysOnTheirDecodedName: two spellings of one key name the
// same member, and the spelling found on disk is the one written back.
func TestMergeJSONMatchesKeysOnTheirDecodedName(t *testing.T) {
	got := mergeJSON(t, `{"abc":"new"}`, `{"\u0061bc":"old","other":1}`, p("abc"))
	want := "{\n  \"\\u0061bc\": \"new\",\n  \"other\": 1\n}\n"
	if string(got.Document) != want {
		t.Errorf("gave %q, want %q", got.Document, want)
	}
	// An owned path matches the decoded name as well.
	got = mergeJSON(t, `{"\u0061bc":"new"}`, `{"abc":"old"}`, p("abc"))
	want = "{\n  \"abc\": \"new\"\n}\n"
	if string(got.Document) != want {
		t.Errorf("gave %q, want %q", got.Document, want)
	}
}

// TestMergeJSONEmptyAndDottedKeys: the empty string is a legal key and a dot is
// part of a component, not a separator.
func TestMergeJSONEmptyAndDottedKeys(t *testing.T) {
	got := mergeJSON(t, `{"":{"a.b":1}}`, `{"":{"a.b":0,"a":{"b":9}},"x":1}`, p("", "a.b"))
	want := "{\n  \"\": {\n    \"a.b\": 1,\n    \"a\": {\n      \"b\": 9\n    }\n  },\n  \"x\": 1\n}\n"
	if string(got.Document) != want {
		t.Errorf("gave %q, want %q", got.Document, want)
	}
}

// TestMergeJSONNestedDuplicateKeyIsReplacedByTheDeclaredValue: only the
// top-level object has to be readable. A found member the desired document
// declares and whose object names a key twice cannot be merged member by
// member, so the declared value is written, and a note says it was replaced.
func TestMergeJSONNestedDuplicateKeyIsReplacedByTheDeclaredValue(t *testing.T) {
	got := mergeJSON(t, `{"fixture-a":{"x":1}}`, `{"fixture-a":{"y":1,"y":2},"fixture-b":1}`, p("fixture-a", "x"))
	if got.Outcome != keymerge.OutcomeMerged {
		t.Fatalf("outcome = %q, want merged", got.Outcome)
	}
	want := "{\n  \"fixture-a\": {\n    \"x\": 1\n  },\n  \"fixture-b\": 1\n}\n"
	if string(got.Document) != want {
		t.Errorf("gave %q, want %q", got.Document, want)
	}
	if n := notesOf(got); !sameStrings(n, []string{"type_conflict fixture-a"}) {
		t.Errorf("notes = %v, want a type_conflict at fixture-a", n)
	}
	// A duplicate inside a member the desired document does not declare stands.
	got = mergeJSON(t, `{"fixture-a":1}`, `{"fixture-b":{"y":1,"y":2}}`, p("fixture-a"))
	want = "{\n  \"fixture-b\": {\n    \"y\": 1,\n    \"y\": 2\n  },\n  \"fixture-a\": 1\n}\n"
	if string(got.Document) != want {
		t.Errorf("gave %q, want %q", got.Document, want)
	}
}

// TestMergeJSONDesiredNestedDuplicateIsACopiedLeaf: a declared member whose
// own object names a key twice cannot be walked, so it is copied as written.
func TestMergeJSONDesiredNestedDuplicateIsACopiedLeaf(t *testing.T) {
	got := mergeJSON(t, `{"fixture-a":{"x":1,"x":2}}`, `{"fixture-a":{"y":1}}`, p("fixture-a"))
	want := "{\n  \"fixture-a\": {\n    \"x\": 1,\n    \"x\": 2\n  }\n}\n"
	if string(got.Document) != want {
		t.Errorf("gave %q, want %q", got.Document, want)
	}
}

// TestMergeJSONOwnership is what the owned paths add to the rule: a declared
// key the caller does not own is never overwritten and never added.
func TestMergeJSONOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		desired  string
		existing string
		owned    []keymerge.KeyPath
		want     string
		notes    []string
	}{{
		name:     "a declared key that is not owned keeps the found value",
		desired:  `{"fixture-model":"slot","fixture-mode":"auto"}`,
		existing: `{"fixture-model":"operator","fixture-mode":"plan"}`,
		owned:    []keymerge.KeyPath{p("fixture-mode")},
		want:     "{\n  \"fixture-model\": \"operator\",\n  \"fixture-mode\": \"auto\"\n}\n",
		notes:    []string{"unowned_kept fixture-model"},
	}, {
		name:     "a declared key that is not owned and equal after white space produces no note",
		desired:  `{"fixture-model":{"a":[1, 2]},"fixture-mode":"auto"}`,
		existing: `{"fixture-model":{ "a" : [1,2] },"fixture-mode":"plan"}`,
		owned:    []keymerge.KeyPath{p("fixture-mode")},
		want:     "{\n  \"fixture-model\": {\n    \"a\": [\n      1,\n      2\n    ]\n  },\n  \"fixture-mode\": \"auto\"\n}\n",
	}, {
		name:     "a declared key that is not owned and absent is skipped",
		desired:  `{"fixture-model":"slot","fixture-mode":"auto"}`,
		existing: `{"fixture-other":1}`,
		owned:    []keymerge.KeyPath{p("fixture-mode")},
		want:     "{\n  \"fixture-other\": 1,\n  \"fixture-mode\": \"auto\"\n}\n",
		notes:    []string{"unowned_skipped fixture-model"},
	}, {
		name:     "an absent owned container is written with its owned leaves only",
		desired:  `{"fixture-srv":{"command":"run","args":["a"],"slot":"keep","deep":{"x":1,"y":2}}}`,
		existing: `{"fixture-other":1}`,
		owned:    []keymerge.KeyPath{p("fixture-srv", "command"), p("fixture-srv", "args"), p("fixture-srv", "deep", "y")},
		want:     "{\n  \"fixture-other\": 1,\n  \"fixture-srv\": {\n    \"command\": \"run\",\n    \"args\": [\n      \"a\"\n    ],\n    \"deep\": {\n      \"y\": 2\n    }\n  }\n}\n",
		notes:    []string{"unowned_skipped fixture-srv/slot", "unowned_skipped fixture-srv/deep/x"},
	}, {
		name:     "a found object composes and an unowned declared member inside it stands",
		desired:  `{"fixture-srv":{"command":"run","slot":"declared"}}`,
		existing: `{"fixture-srv":{"command":"old","slot":"operator","extra":true}}`,
		owned:    []keymerge.KeyPath{p("fixture-srv", "command")},
		want:     "{\n  \"fixture-srv\": {\n    \"command\": \"run\",\n    \"slot\": \"operator\",\n    \"extra\": true\n  }\n}\n",
		notes:    []string{"unowned_kept fixture-srv/slot"},
	}, {
		name:     "an owned object over a found scalar is written pruned, with a note",
		desired:  `{"fixture-srv":{"command":"run","slot":"declared"}}`,
		existing: `{"fixture-srv":"scalar"}`,
		owned:    []keymerge.KeyPath{p("fixture-srv", "command")},
		want:     "{\n  \"fixture-srv\": {\n    \"command\": \"run\"\n  }\n}\n",
		notes:    []string{"type_conflict fixture-srv", "unowned_skipped fixture-srv/slot"},
	}, {
		name:     "an unowned object over a found scalar leaves the scalar",
		desired:  `{"fixture-srv":{"slot":"declared"},"fixture-mode":"auto"}`,
		existing: `{"fixture-srv":"scalar"}`,
		owned:    []keymerge.KeyPath{p("fixture-mode")},
		want:     "{\n  \"fixture-srv\": \"scalar\",\n  \"fixture-mode\": \"auto\"\n}\n",
		notes:    []string{"unowned_kept fixture-srv"},
	}, {
		name:     "an owned path with no declared value changes nothing",
		desired:  `{"fixture-mode":"auto"}`,
		existing: `{"fixture-mode":"plan","fixture-gone":"operator"}`,
		owned:    []keymerge.KeyPath{p("fixture-mode"), p("fixture-gone")},
		want:     "{\n  \"fixture-mode\": \"auto\",\n  \"fixture-gone\": \"operator\"\n}\n",
	}, {
		name:     "an owned path that only passes through a declared leaf owns nothing there",
		desired:  `{"fixture-a":5}`,
		existing: `{"fixture-a":{"b":1}}`,
		owned:    []keymerge.KeyPath{p("fixture-a", "b")},
		want:     "{\n  \"fixture-a\": {\n    \"b\": 1\n  }\n}\n",
		notes:    []string{"unowned_kept fixture-a"},
	}, {
		name:     "an owned empty object over a found object leaves its members",
		desired:  `{"fixture-env":{},"fixture-slot":1}`,
		existing: `{"fixture-env":{"A":"1"},"fixture-slot":2}`,
		owned:    []keymerge.KeyPath{p("fixture-env")},
		want:     "{\n  \"fixture-env\": {\n    \"A\": \"1\"\n  },\n  \"fixture-slot\": 2\n}\n",
		notes:    []string{"unowned_kept fixture-slot"},
	}, {
		name:     "an owned scalar replacing a found object is noted",
		desired:  `{"fixture-a":1}`,
		existing: `{"fixture-a":{"x":1}}`,
		owned:    []keymerge.KeyPath{p("fixture-a")},
		want:     "{\n  \"fixture-a\": 1\n}\n",
		notes:    []string{"type_conflict fixture-a"},
	}, {
		name:     "an owned array replacing a found object is noted",
		desired:  `{"fixture-a":[1]}`,
		existing: `{"fixture-a":{"x":1}}`,
		owned:    []keymerge.KeyPath{p("fixture-a")},
		want:     "{\n  \"fixture-a\": [\n    1\n  ]\n}\n",
		notes:    []string{"type_conflict fixture-a"},
	}, {
		name:     "an owned array replacing a found scalar is not a conflict",
		desired:  `{"fixture-a":[1]}`,
		existing: `{"fixture-a":"s"}`,
		owned:    []keymerge.KeyPath{p("fixture-a")},
		want:     "{\n  \"fixture-a\": [\n    1\n  ]\n}\n",
	}, {
		name:     "an owned scalar replacing a found object that cannot be read is noted",
		desired:  `{"fixture-a":1}`,
		existing: `{"fixture-a":{"x":1,"x":2}}`,
		owned:    []keymerge.KeyPath{p("fixture-a")},
		want:     "{\n  \"fixture-a\": 1\n}\n",
		notes:    []string{"type_conflict fixture-a"},
	}, {
		name:     "an owned object that cannot be walked over a found scalar is noted",
		desired:  `{"fixture-a":{"x":1,"x":2}}`,
		existing: `{"fixture-a":1}`,
		owned:    []keymerge.KeyPath{p("fixture-a")},
		want:     "{\n  \"fixture-a\": {\n    \"x\": 1,\n    \"x\": 2\n  }\n}\n",
		notes:    []string{"type_conflict fixture-a"},
	}, {
		name:     "an owned object that cannot be walked over a found object is noted",
		desired:  `{"fixture-a":{"x":1,"x":2}}`,
		existing: `{"fixture-a":{"y":1}}`,
		owned:    []keymerge.KeyPath{p("fixture-a")},
		want:     "{\n  \"fixture-a\": {\n    \"x\": 1,\n    \"x\": 2\n  }\n}\n",
		notes:    []string{"type_conflict fixture-a"},
	}, {
		name:     "nothing owned writes nothing",
		desired:  `{"fixture-a":1,"fixture-b":{"c":2}}`,
		existing: `{"fixture-z":0}`,
		owned:    nil,
		want:     "{\n  \"fixture-z\": 0\n}\n",
		notes:    []string{"unowned_skipped fixture-a", "unowned_skipped fixture-b"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeJSON(t, tc.desired, tc.existing, tc.owned...)
			if got.Outcome != keymerge.OutcomeMerged {
				t.Fatalf("outcome = %q/%q, want merged", got.Outcome, got.Reason)
			}
			if string(got.Document) != tc.want {
				t.Errorf("gave\n\t%q\nwant\n\t%q", got.Document, tc.want)
			}
			if n := notesOf(got); !sameStrings(n, tc.notes) {
				t.Errorf("notes = %v, want %v", n, tc.notes)
			}
		})
	}
}

// TestMergeJSONNotesFollowTheDeclaredDocument: notes come in the order the
// desired document declares the keys, whatever order the found document has.
func TestMergeJSONNotesFollowTheDeclaredDocument(t *testing.T) {
	got := mergeJSON(t,
		`{"fixture-c":1,"fixture-a":1,"fixture-b":1}`,
		`{"fixture-b":2,"fixture-a":2,"fixture-c":2}`)
	if n := notesOf(got); !sameStrings(n, []string{"unowned_kept fixture-c", "unowned_kept fixture-a", "unowned_kept fixture-b"}) {
		t.Errorf("notes = %v, want declared order c, a, b", n)
	}
	// A note's path uses decoded names and is detached from the other notes.
	got = mergeJSON(t, `{"\u0061":{"b":1}}`, `{"a":{"b":2}}`)
	if len(got.Notes) != 1 || !sameStrings(got.Notes[0].Path, []string{"a"}) {
		t.Fatalf("notes = %v, want one note at a", got.Notes)
	}
}

// TestMergeJSONRefusesUnusableOwnedPaths: an owned path that cannot be honored
// is a loud error, checked in a fixed order.
func TestMergeJSONRefusesUnusableOwnedPaths(t *testing.T) {
	want := &keymerge.Error{Code: keymerge.CodeInvalidOwnedPaths}
	for _, tc := range []struct {
		name     string
		desired  string
		existing string
		owned    []keymerge.KeyPath
		reason   keymerge.Reason
	}{
		{"a path with no components", `{"a":1}`, `{"a":2}`, []keymerge.KeyPath{{}}, keymerge.ReasonEmptyPath},
		{"a nil path", `{"a":1}`, `{"a":2}`, []keymerge.KeyPath{nil}, keymerge.ReasonEmptyPath},
		{"a path naming a non-empty object", `{"a":{"b":1}}`, `{"a":{"b":2}}`, []keymerge.KeyPath{p("a")}, keymerge.ReasonNotLeaf},
		{"a path naming a non-empty object, found unreadable", `{"a":{"b":1}}`, `not json`, []keymerge.KeyPath{p("a")}, keymerge.ReasonNotLeaf},
		{"a bad path outranks an unreadable desired document", `not json`, `{}`, []keymerge.KeyPath{{}}, keymerge.ReasonEmptyPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := keymerge.MergeJSON([]byte(tc.desired), []byte(tc.existing), tc.owned)
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			var typed *keymerge.Error
			if !errors.As(err, &typed) || typed.Reason != tc.reason {
				t.Errorf("error = %#v, want reason %q", typed, tc.reason)
			}
		})
	}
	// These are not errors: the path is not honored, but nothing is wrong with it.
	for _, tc := range []struct{ name, desired string }{
		{"a path to a key the desired document lacks", `{"b":1}`},
		{"a path through a declared leaf", `{"a":1}`},
		{"a path to an empty object", `{"a":{}}`},
		{"a path through a nested duplicate the desired document cannot walk", `{"a":{"x":1,"x":2}}`},
	} {
		if _, err := keymerge.MergeJSON([]byte(tc.desired), []byte(`{}`), []keymerge.KeyPath{p("a"), p("a", "b")}); err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
	}
	// An unreadable desired document hides the owned paths from the check.
	got, err := keymerge.MergeJSON([]byte(`[`), []byte(`{}`), []keymerge.KeyPath{p("a")})
	if err != nil || got.Outcome != keymerge.OutcomeDesiredUnreadable {
		t.Errorf("unreadable desired = %q, %v, want the desired_unreadable outcome", got.Outcome, err)
	}
}

// TestMergeJSONIsIdempotentAndDeterministic: installing twice writes the same
// bytes the second time, and a repeated call answers identically.
func TestMergeJSONIsIdempotentAndDeterministic(t *testing.T) {
	owned := []keymerge.KeyPath{p("fixture-model"), p("fixture-env", "A")}
	const desired = `{"fixture-model":"opus","fixture-env":{"A":"1"},"fixture-slot":"s"}`
	const existing = `{"fixture-tui":"x","fixture-model":"haiku","fixture-env":{"B":"2"}}`
	once := mergeJSON(t, desired, existing, owned...)
	twice := mergeJSON(t, desired, string(once.Document), owned...)
	if !bytes.Equal(once.Document, twice.Document) {
		t.Errorf("merging twice moved the document:\n\tonce  %q\n\ttwice %q", once.Document, twice.Document)
	}
	for i := 0; i < 20; i++ {
		again := mergeJSON(t, desired, existing, owned...)
		if !bytes.Equal(again.Document, once.Document) || !sameStrings(notesOf(again), notesOf(once)) {
			t.Fatalf("call %d answered differently", i)
		}
	}
}

// TestMergeJSONNeverTouchesItsInputsAndReturnsDetachedOutput.
func TestMergeJSONNeverTouchesItsInputsAndReturnsDetachedOutput(t *testing.T) {
	desired := []byte(`{"fixture-a":"x","fixture-b":{"c":1}}`)
	existing := []byte(`{"fixture-z":"keep","fixture-a":"old"}`)
	owned := []keymerge.KeyPath{p("fixture-a"), p("fixture-b", "c")}
	ownedBefore := []keymerge.KeyPath{p("fixture-a"), p("fixture-b", "c")}
	d0, e0 := bytes.Clone(desired), bytes.Clone(existing)

	got, err := keymerge.MergeJSON(desired, existing, owned)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(desired, d0) || !bytes.Equal(existing, e0) {
		t.Fatal("an input was modified")
	}
	for i := range owned {
		if !sameStrings(owned[i], ownedBefore[i]) {
			t.Fatal("the owned paths were modified")
		}
	}
	// Scribbling over the output reaches neither input, nor a later call.
	for i := range got.Document {
		got.Document[i] = 'X'
	}
	if !bytes.Equal(desired, d0) || !bytes.Equal(existing, e0) {
		t.Fatal("the output shares memory with an input")
	}
	again, _ := keymerge.MergeJSON(desired, existing, owned)
	if bytes.Contains(again.Document, []byte("X")) {
		t.Fatal("the output shares memory with a later call")
	}

	// The same holds where the desired bytes come back untouched.
	for _, existing := range [][]byte{nil, []byte("not json")} {
		got, _ = keymerge.MergeJSON(desired, existing, owned)
		if !bytes.Equal(got.Document, desired) {
			t.Fatalf("existing %q: expected the desired bytes back", existing)
		}
		got.Document[0] = 'X'
		if !bytes.Equal(desired, d0) {
			t.Fatalf("existing %q: the returned desired bytes alias the input", existing)
		}
	}
	got, _ = keymerge.MergeJSON([]byte(`[`), existing, owned)
	got.Document[0] = 'X'
	if !bytes.Equal(existing, e0) {
		t.Fatal("an unreadable desired document came back aliased")
	}
}

// TestMergeJSONNoteBounds: notes are bounded by what the desired document
// declares, never by the found document.
func TestMergeJSONNoteBounds(t *testing.T) {
	var found strings.Builder
	found.WriteString(`{`)
	for i := 0; i < 2000; i++ {
		if i > 0 {
			found.WriteString(`,`)
		}
		fmt.Fprintf(&found, `"k%d":1`, i)
	}
	found.WriteString(`}`)
	got := mergeJSON(t, `{"fixture-a":1,"fixture-b":2}`, found.String())
	if got.Outcome != keymerge.OutcomeMerged {
		t.Fatalf("outcome = %q, want merged", got.Outcome)
	}
	if len(got.Notes) > 2 {
		t.Errorf("%d notes for a desired document of two keys", len(got.Notes))
	}
}

// TestMergeJSONNotePathsOfSiblingsAreIndependent: notes for sibling keys deep in
// a document each carry their own path.
func TestMergeJSONNotePathsOfSiblingsAreIndependent(t *testing.T) {
	got := mergeJSON(t,
		`{"a":{"b":{"c":{"x":1,"y":2,"w":3}}}}`,
		`{"a":{"b":{"c":{"x":9,"y":8,"w":7}}}}`,
		p("a", "b", "c", "w"))
	if n := notesOf(got); !sameStrings(n, []string{"unowned_kept a/b/c/x", "unowned_kept a/b/c/y"}) {
		t.Errorf("notes = %v, want the two siblings with their own paths", n)
	}
	// Mutating one note's path reaches no other note.
	got.Notes[0].Path[0] = "changed"
	if got.Notes[1].Path[0] != "a" {
		t.Error("two notes share a path")
	}
}
