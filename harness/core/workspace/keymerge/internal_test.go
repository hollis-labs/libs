package keymerge

import (
	"testing"
)

// TestQuotedKey pins the guard that hands raw bytes back into a document: the
// span between two decoder offsets must be white space, at most one comma, white
// space and a quoted key, and anything else is refused. The decoder never
// produces any other span, so these cases are the only way to reach the refusals.
func TestQuotedKey(t *testing.T) {
	for _, tc := range []struct {
		span string
		want string
	}{
		{`"k"`, `"k"`},
		{"  \"k\"", `"k"`},
		{` , "k"`, `"k"`},
		{",\n\t\"k\"", `"k"`},
		{`"a b"`, `"a b"`},
		{`""`, `""`},
		{``, ``},
		{`,`, ``},
		{`"`, ``},
		{`,"`, ``},
		{`k`, ``},
		{`'k'`, ``},
		{`,,"k"`, ``},
		{`"k`, ``},
		{`k"`, ``},
		{`"k":`, ``},
	} {
		got := quotedKey([]byte(tc.span))
		if (tc.want == "") != (got == nil) || string(got) != tc.want {
			t.Errorf("quotedKey(%q) = %q, want %q", tc.span, got, tc.want)
		}
	}
}

// TestLayoutJSON pins the layout rule, including the two inputs a merge never
// produces but a comparison can hand it: bytes that are not JSON, and white
// space around the document.
func TestLayoutJSON(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{`{"a":1,"b":[1,2],"c":{}}`, "{\n  \"a\": 1,\n  \"b\": [\n    1,\n    2\n  ],\n  \"c\": {}\n}\n"},
		{" \n\t{\"a\":1} \r\n", "{\n  \"a\": 1\n}\n"},
		{"{\n  \"a\": 1\n}\n", "{\n  \"a\": 1\n}\n"},
		{"  not json \n", "not json\n"},
		{`{"a":1} trailing`, "{\"a\":1} trailing\n"},
		{" {\"a\":1} ", "{\n  \"a\": 1\n}\n"},
		{"", "\n"},
		{`{"a":"<&>"}`, "{\n  \"a\": \"<&>\"\n}\n"},
	} {
		if got := string(layoutJSON([]byte(tc.in))); got != tc.want {
			t.Errorf("layoutJSON(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMarshalTreeRefusal pins the fallback for an encoder failure. The encoder
// cannot fail on a tree its own decoder produced, so a merge never reaches it;
// the guard is pinned here on the values it refuses.
func TestMarshalTreeRefusal(t *testing.T) {
	for name, tree := range map[string]any{
		"an unsupported kind": map[string]any{"a": make(chan int)},
		"a nil in an array":   map[string]any{"a": []any{nil}},
	} {
		if out, ok := marshalTree(tree); ok || out != nil {
			t.Errorf("%s: marshalTree = %q, %v, want a refusal", name, out, ok)
		}
	}
	out, ok := marshalTree(map[string]any{"a": int64(1)})
	if !ok || string(out) != "a = 1\n" {
		t.Errorf("marshalTree of a decoded value = %q, %v", out, ok)
	}
}

// TestEncoderFallbacks pins what the two callers of the encoder do when it
// refuses a tree, which a tree built by the decoder never makes it do: a merge
// writes the desired document as given and a normalization returns the bytes it
// was given, both in copies.
func TestEncoderFallbacks(t *testing.T) {
	refused := map[string]any{"a": make(chan int)}
	desired := []byte("a = 1\n")
	out := encodeTree(refused, desired)
	if string(out) != "a = 1\n" {
		t.Errorf("encodeTree fallback = %q, want the desired document", out)
	}
	out[0] = 'X'
	if string(desired) != "a = 1\n" {
		t.Error("the fallback aliases the desired document")
	}
	if got := encodeTree(map[string]any{"a": int64(2)}, desired); string(got) != "a = 2\n" {
		t.Errorf("encodeTree = %q, want the encoded tree", got)
	}

	raw := []byte("b = 1\n")
	norm, ok := normalizeTree(refused, raw)
	if ok || string(norm) != "b = 1\n" {
		t.Errorf("normalizeTree fallback = %q, %v, want the raw bytes and false", norm, ok)
	}
	norm[0] = 'X'
	if string(raw) != "b = 1\n" {
		t.Error("the fallback aliases the raw bytes")
	}
	if got, ok := normalizeTree(map[string]any{"b": int64(3)}, raw); !ok || string(got) != "b = 3\n" {
		t.Errorf("normalizeTree = %q, %v, want the encoded tree and true", got, ok)
	}
}

// TestOwnedSet: a nil set owns nothing, paths are exact, and a component is a
// whole key whatever characters it holds.
func TestOwnedSet(t *testing.T) {
	var none *ownedSet
	if none.child("a") != nil || none.owns() {
		t.Error("a nil set owns something")
	}
	set, err := newOwnedSet([]KeyPath{{"a", "b"}, {"a", "b"}, {"c.d"}, {""}, {"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if set.owns() {
		t.Error("the root of the paths is owned")
	}
	if !set.child("a").owns() || !set.child("a").child("b").owns() {
		t.Error("a and a/b should both be owned")
	}
	if set.child("a").child("c") != nil {
		t.Error("a/c is not on any owned path")
	}
	if !set.child("c.d").owns() || set.child("c") != nil || set.child("d") != nil {
		t.Error("a dot is part of a component, not a separator")
	}
	if !set.child("").owns() {
		t.Error("the empty key is a key")
	}
	if _, err := newOwnedSet([]KeyPath{{"a"}, {}}); err == nil {
		t.Error("a path with no components was accepted")
	}
	if set, err := newOwnedSet(nil); err != nil || set.child("a") != nil {
		t.Error("no paths should own nothing and be fine")
	}
}

// TestErrorText: the text is fixed by code and reason, never by a document.
func TestErrorText(t *testing.T) {
	for _, tc := range []struct {
		err  *Error
		want string
	}{
		{&Error{Code: CodeExistingTOMLInvalid, Reason: ReasonNotTOML, Line: 3, Column: 4}, "keymerge: existing_toml_invalid: not_toml"},
		{&Error{Code: CodeInvalidOwnedPaths}, "keymerge: invalid_owned_paths"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
	var none *Error
	if (&Error{Code: CodeInvalidOwnedPaths}).Is(none) || (&Error{Code: CodeInvalidOwnedPaths}).Is(nil) {
		t.Error("Is matched a nil target")
	}
	if (&Error{Code: CodeInvalidOwnedPaths}).Is(&Error{Code: CodeExistingTOMLInvalid}) {
		t.Error("Is matched a different code")
	}
}
