package keymerge_test

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
	"github.com/pelletier/go-toml/v2"
)

// The properties below run the merges over seeded random documents and compare
// them with a model written on decoded values, which is a different program
// from the merges: it sees no bytes, no order and no notes. The generator is
// seeded, so a failure reproduces.

type member struct {
	key   string
	value any
}

// object is an ordered JSON object; the order is what the document is written in.
type object []member

var keyPool = []string{"a", "b", "c", "d", "e", "f.g", "", "h<i", "j k"}

func genKeys(r *rand.Rand, max int) []string {
	n := 1 + r.IntN(max)
	picked := r.Perm(len(keyPool))[:n]
	keys := make([]string, n)
	for i, k := range picked {
		keys[i] = keyPool[k]
	}
	return keys
}

// genValue returns an ordered tree: object for a JSON object, []any, string,
// float64, bool, nil.
func genValue(r *rand.Rand, depth int, forTOML bool) any {
	for {
		switch pick := r.IntN(100); {
		case pick < 35 && depth < 3:
			var o object
			for _, k := range genKeys(r, 4) {
				o = append(o, member{k, genValue(r, depth+1, forTOML)})
			}
			return o
		case pick < 40:
			return object{}
		case pick < 50:
			if forTOML {
				return []any{"x", "y"}
			}
			return []any{float64(r.IntN(9)), "s", object{{"k", float64(1)}}}
		case pick < 65:
			return []string{"one", "two", "three", "four"}[r.IntN(4)]
		case pick < 80:
			return float64(r.IntN(5))
		case pick < 90:
			return r.IntN(2) == 0
		default:
			if forTOML {
				continue
			}
			return nil
		}
	}
}

func genDocument(r *rand.Rand, forTOML bool) object {
	var o object
	for _, k := range genKeys(r, 5) {
		o = append(o, member{k, genValue(r, 1, forTOML)})
	}
	return o
}

// writeJSON serializes an ordered tree, with white space chosen at random.
func writeJSON(r *rand.Rand, v any) string {
	space := func() string { return []string{"", " ", "\n  ", "\t"}[r.IntN(4)] }
	switch x := v.(type) {
	case object:
		var b strings.Builder
		b.WriteString("{" + space())
		for i, m := range x {
			if i > 0 {
				b.WriteString("," + space())
			}
			key, _ := json.Marshal(m.key)
			b.Write(key)
			b.WriteString(space() + ":" + space())
			b.WriteString(writeJSON(r, m.value))
		}
		b.WriteString(space() + "}")
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString("," + space())
			}
			b.WriteString(writeJSON(r, e))
		}
		b.WriteString("]")
		return b.String()
	default:
		out, _ := json.Marshal(x)
		return string(out)
	}
}

// decodeJSON decodes a document into maps, as the model reads it.
func decodeJSON(t *testing.T, doc string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatalf("decode %q: %v", doc, err)
	}
	return out
}

func isLeaf(v any) bool {
	m, ok := v.(map[string]any)
	return !ok || len(m) == 0
}

// leaves lists the path of every leaf of a decoded document.
func leaves(m map[string]any, prefix []string, out *[]keymerge.KeyPath) {
	for k, v := range m {
		path := append(append([]string(nil), prefix...), k)
		if isLeaf(v) {
			*out = append(*out, path)
			continue
		}
		leaves(v.(map[string]any), path, out)
	}
}

func pathKey(path []string) string { return strings.Join(path, "\x00") }

// The model of the rule, on decoded values.

func modelClaimed(path []string, v any, owned map[string]bool) bool {
	if isLeaf(v) {
		return owned[pathKey(path)]
	}
	for k, c := range v.(map[string]any) {
		if modelClaimed(append(append([]string(nil), path...), k), c, owned) {
			return true
		}
	}
	return false
}

func modelPrune(path []string, v any, owned map[string]bool) any {
	if isLeaf(v) {
		return v
	}
	out := map[string]any{}
	for k, c := range v.(map[string]any) {
		p := append(append([]string(nil), path...), k)
		if modelClaimed(p, c, owned) {
			out[k] = modelPrune(p, c, owned)
		}
	}
	return out
}

func modelMerge(desired, found map[string]any, prefix []string, owned map[string]bool) map[string]any {
	out := map[string]any{}
	for k, v := range found {
		out[k] = v
	}
	for k, dv := range desired {
		path := append(append([]string(nil), prefix...), k)
		fv, present := found[k]
		claimed := modelClaimed(path, dv, owned)
		switch {
		case !present && !claimed:
		case !present:
			out[k] = modelPrune(path, dv, owned)
		case !claimed:
		default:
			dm, dIsMap := dv.(map[string]any)
			fm, fIsMap := fv.(map[string]any)
			switch {
			case dIsMap && len(dm) > 0 && fIsMap:
				out[k] = modelMerge(dm, fm, path, owned)
			case dIsMap && len(dm) == 0 && fIsMap:
				out[k] = fv
			default:
				out[k] = modelPrune(path, dv, owned)
			}
		}
	}
	return out
}

// cairnMerge is the whole of the original rule: every declared key wins, at
// every depth, and two objects compose.
func cairnMerge(desired, found map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range found {
		out[k] = v
	}
	for k, dv := range desired {
		fv, present := found[k]
		dm, dIsMap := dv.(map[string]any)
		fm, fIsMap := fv.(map[string]any)
		if present && dIsMap && fIsMap {
			out[k] = cairnMerge(dm, fm)
			continue
		}
		out[k] = dv
	}
	return out
}

// ownedChoice returns a random owned set drawn from the desired document's
// leaves, with an occasional path the desired document does not have.
func ownedChoice(r *rand.Rand, desired map[string]any, all bool) ([]keymerge.KeyPath, map[string]bool) {
	var every []keymerge.KeyPath
	leaves(desired, nil, &every)
	var chosen []keymerge.KeyPath
	for _, p := range every {
		if all || r.IntN(2) == 0 {
			chosen = append(chosen, p)
		}
	}
	if !all && r.IntN(4) == 0 {
		chosen = append(chosen, keymerge.KeyPath{keyPool[r.IntN(len(keyPool))], "zz"})
	}
	set := map[string]bool{}
	for _, p := range chosen {
		set[pathKey(p)] = true
	}
	return chosen, set
}

func topLevelKeys(t *testing.T, doc []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestPropertyMergeJSON(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	var narrowed, conflicts int
	for i := 0; i < 4000; i++ {
		desiredText := writeJSON(r, genDocument(r, false))
		existingText := writeJSON(r, genDocument(r, false))
		desired, existing := decodeJSON(t, desiredText), decodeJSON(t, existingText)
		for _, all := range []bool{true, false} {
			owned, set := ownedChoice(r, desired, all)
			d0, e0 := []byte(desiredText), []byte(existingText)
			got, err := keymerge.MergeJSON(d0, e0, owned)
			if err != nil {
				t.Fatalf("case %d: %v\ndesired %s\nexisting %s\nowned %v", i, err, desiredText, existingText, owned)
			}
			if string(d0) != desiredText || string(e0) != existingText {
				t.Fatalf("case %d: an input was modified", i)
			}
			if got.Outcome != keymerge.OutcomeMerged {
				t.Fatalf("case %d: outcome %q/%q for readable documents", i, got.Outcome, got.Reason)
			}
			out := decodeJSON(t, string(got.Document))
			if want := modelMerge(desired, existing, nil, set); !reflect.DeepEqual(out, want) {
				t.Fatalf("case %d (all=%v): the merge differs from the model\ndesired  %s\nexisting %s\nowned    %v\ngot      %s\nwant     %v",
					i, all, desiredText, existingText, owned, got.Document, want)
			}
			if all {
				if want := cairnMerge(desired, existing); !reflect.DeepEqual(out, want) {
					t.Fatalf("case %d: owning every declared leaf differs from the original rule\ndesired  %s\nexisting %s\ngot      %s",
						i, desiredText, existingText, got.Document)
				}
			} else {
				narrowed++
			}
			// Found top-level keys keep their order; new keys follow them.
			foundKeys := topLevelKeys(t, e0)
			gotKeys := topLevelKeys(t, got.Document)
			if len(gotKeys) < len(foundKeys) || !reflect.DeepEqual(gotKeys[:len(foundKeys)], foundKeys) {
				t.Fatalf("case %d: found key order %v became %v", i, foundKeys, gotKeys)
			}
			// Deterministic, and merging the result again changes nothing.
			again, _ := keymerge.MergeJSON(d0, e0, owned)
			if !bytes.Equal(again.Document, got.Document) || !reflect.DeepEqual(again.Notes, got.Notes) {
				t.Fatalf("case %d: a second call answered differently", i)
			}
			twice, err := keymerge.MergeJSON(d0, got.Document, owned)
			if err != nil || !bytes.Equal(twice.Document, got.Document) {
				t.Fatalf("case %d: merging the result moved it\nonce  %q\ntwice %q (%v)", i, got.Document, twice.Document, err)
			}
			// A note is a decision about a declared key, so there are no more
			// notes than declared keys.
			var count func(m map[string]any)
			total := 0
			count = func(m map[string]any) {
				for _, v := range m {
					total++
					if sub, ok := v.(map[string]any); ok {
						count(sub)
					}
				}
			}
			count(desired)
			if len(got.Notes) > total {
				t.Fatalf("case %d: %d notes for %d declared keys", i, len(got.Notes), total)
			}
			for _, n := range got.Notes {
				if n.Code == keymerge.NoteTypeConflict {
					conflicts++
				}
			}
		}
	}
	if narrowed == 0 || conflicts == 0 {
		t.Fatalf("the generator never produced a narrowed set (%d) or a type conflict (%d)", narrowed, conflicts)
	}
}

func TestPropertyMergeTOML(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 17))
	toTree := func(o object) map[string]any {
		var build func(v any) any
		build = func(v any) any {
			switch x := v.(type) {
			case object:
				m := map[string]any{}
				for _, mem := range x {
					m[mem.key] = build(mem.value)
				}
				return m
			case float64:
				return int64(x)
			default:
				return x
			}
		}
		return build(o).(map[string]any)
	}
	encode := func(m map[string]any) string {
		out, err := toml.Marshal(m)
		if err != nil {
			t.Fatalf("encode %v: %v", m, err)
		}
		return string(out)
	}
	decode := func(doc string) map[string]any {
		var m map[string]any
		if err := toml.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatalf("decode %q: %v", doc, err)
		}
		return m
	}
	for i := 0; i < 3000; i++ {
		desiredText := encode(toTree(genDocument(r, true)))
		existingText := encode(toTree(genDocument(r, true)))
		desired, existing := decode(desiredText), decode(existingText)
		for _, all := range []bool{true, false} {
			owned, set := ownedChoice(r, desired, all)
			d0, e0 := []byte(desiredText), []byte(existingText)
			got, err := keymerge.MergeTOML(d0, e0, owned)
			if err != nil {
				t.Fatalf("case %d: %v\ndesired %s\nexisting %s\nowned %v", i, err, desiredText, existingText, owned)
			}
			if string(d0) != desiredText || string(e0) != existingText {
				t.Fatalf("case %d: an input was modified", i)
			}
			out := decode(string(got.Document))
			if want := modelMerge(desired, existing, nil, set); !reflect.DeepEqual(out, want) {
				t.Fatalf("case %d (all=%v): the merge differs from the model\ndesired  %s\nexisting %s\nowned    %v\ngot      %s\nwant     %v",
					i, all, desiredText, existingText, owned, got.Document, want)
			}
			if all {
				if want := cairnMerge(desired, existing); !reflect.DeepEqual(out, want) {
					t.Fatalf("case %d: owning every declared leaf differs from the original rule\ndesired  %s\nexisting %s\ngot      %s",
						i, desiredText, existingText, got.Document)
				}
			}
			again, _ := keymerge.MergeTOML(d0, e0, owned)
			if !bytes.Equal(again.Document, got.Document) || !reflect.DeepEqual(again.Notes, got.Notes) {
				t.Fatalf("case %d: a second call answered differently", i)
			}
			twice, err := keymerge.MergeTOML(d0, got.Document, owned)
			if err != nil || !bytes.Equal(twice.Document, got.Document) {
				t.Fatalf("case %d: merging the result moved it\nonce  %q\ntwice %q (%v)", i, got.Document, twice.Document, err)
			}
			// The merged document is its own normal form.
			if norm, ok := keymerge.NormalizeTOML(got.Document); !ok || !bytes.Equal(norm, got.Document) {
				t.Fatalf("case %d: the merged document is not normalized: %q -> %q", i, got.Document, norm)
			}
		}
	}
}
