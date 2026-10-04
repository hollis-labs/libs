package keymerge

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"sort"
)

// MergeJSON returns the JSON document an install writes where one already
// exists. Both documents must be exactly one readable JSON object, member by
// member; see the package documentation for the full contract.
//
// The merge walks the desired document. A declared key the caller owns (an
// owned path is exactly that key, or is a key somewhere beneath it) carries the
// desired value, and a key the document found holds and the desired document
// does not declare stands, wherever it sits, as the raw bytes found. A declared
// key the caller does not own is never overwritten and never added.
//
// A document that cannot be read is an outcome, not an error: the desired
// document comes back untouched. The error is for owned paths that cannot be
// honored (a path with no components, or one naming a non-empty object in the
// desired document); it is checked after the desired document is read and
// before the found one is, so the result does not depend on the found bytes.
func MergeJSON(desired, existing []byte, owned []KeyPath) (JSONMerge, error) {
	set, err := newOwnedSet(owned)
	if err != nil {
		return JSONMerge{}, err
	}
	want, reason := readObject(desired)
	if reason != "" {
		return JSONMerge{Document: bytes.Clone(desired), Outcome: OutcomeDesiredUnreadable, Reason: reason}, nil
	}
	var (
		seq     int
		notLeaf bool
	)
	declared := buildNodes(want, set, nil, &seq, &notLeaf)
	if notLeaf {
		return JSONMerge{}, errNotLeaf()
	}
	found, reason := readObject(existing)
	if reason != "" {
		return JSONMerge{Document: bytes.Clone(desired), Outcome: OutcomeExistingUnreadable, Reason: reason}, nil
	}
	var m jsonMerger
	document := layoutJSON(m.compose(declared, found))
	return JSONMerge{Document: document, Outcome: OutcomeMerged, Notes: m.orderedNotes()}, nil
}

// layoutJSON lays a document out one element per line at two spaces per level,
// ending in a newline. It moves white space between tokens and changes nothing
// else, so key order and the spelling of strings and numbers survive it, which
// is what separates laying a document out from re-encoding it. A value that is
// not JSON is returned trimmed, with the newline.
func layoutJSON(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", "  "); err != nil {
		buf.Reset()
		buf.Write(trimmed)
	}
	return append(buf.Bytes(), '\n')
}

// jsonMember is one member of a JSON object as it was written: the decoded
// name, which is what two documents are matched on, the quoted key exactly as
// found, and the raw value.
type jsonMember struct {
	name  string
	key   []byte
	value []byte
}

// readObject reads raw as one JSON object, member by member in the order they
// were written. The reason is empty when it is exactly one, and otherwise says
// why not, by precedence: empty, not_json, not_object, trailing_content,
// duplicate_key.
//
// A duplicate key is the case worth naming: Go's decoder resolves it silently,
// the last value wins and the document loses a member, and doing that to a
// file an operator also writes would be editing a document that cannot be read.
// The scan carries on after one so a document that is also malformed, or has
// trailing content, is reported as that.
//
// Only the top-level object's keys are checked for duplicates here. The raw key
// is recovered from the decoder's input offsets rather than re-encoded from the
// decoded token, so it keeps whatever spelling the document used.
func readObject(raw []byte) ([]jsonMember, Reason) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, ReasonEmpty
		}
		return nil, ReasonNotJSON
	}
	if tok != json.Delim('{') {
		var first json.RawMessage
		if json.NewDecoder(bytes.NewReader(raw)).Decode(&first) != nil {
			return nil, ReasonNotJSON
		}
		return nil, ReasonNotObject
	}
	var (
		members   []jsonMember
		seen      = make(map[string]struct{})
		duplicate bool
	)
	for dec.More() {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return nil, ReasonNotJSON
		}
		name, ok := tok.(string)
		if !ok {
			return nil, ReasonNotJSON
		}
		if _, again := seen[name]; again {
			duplicate = true
		}
		seen[name] = struct{}{}
		key := quotedKey(raw[start:dec.InputOffset()])
		if key == nil {
			return nil, ReasonNotJSON
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, ReasonNotJSON
		}
		members = append(members, jsonMember{name: name, key: key, value: value})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, ReasonNotJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ReasonTrailingContent
	}
	if duplicate {
		return nil, ReasonDuplicateKey
	}
	return members, ""
}

// quotedKey returns the quoted key inside the span between two decoder
// offsets, or nil when the span holds anything else.
//
// The span is white space, at most one comma, white space and the key. It is
// verified rather than assumed: this hands raw bytes back into a document, and
// a span that is not what it is expected to be should fail the read rather than
// be written.
func quotedKey(span []byte) []byte {
	key := bytes.TrimSpace(span)
	if len(key) > 0 && key[0] == ',' {
		key = bytes.TrimSpace(key[1:])
	}
	if len(key) < 2 || key[0] != '"' || key[len(key)-1] != '"' {
		return nil
	}
	return key
}

// isObjectKind reports whether a raw value is written as an object. Values
// copied out of the decoder carry no leading white space.
func isObjectKind(raw []byte) bool { return len(raw) > 0 && raw[0] == '{' }

// sameJSON reports whether two raw values are the same once the white space
// between tokens is removed. Spelling is not forgiven.
func sameJSON(a, b []byte) bool {
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(x.Bytes(), y.Bytes())
}

// jsonNode is one member of the desired document, with what the merge needs to
// know about it before it looks at the document found.
type jsonNode struct {
	member jsonMember
	// path is the decoded names from the root to this member.
	path []string
	// seq is the member's place in a pre-order walk of the desired document; it
	// orders the notes.
	seq int
	// children is set when the value is a readable, non-empty object.
	children []*jsonNode
	// empty is set when the value is a readable object with no members.
	empty bool
	// claimed is true when the merge writes this member: it is an owned leaf,
	// or an object with a claimed member beneath it.
	claimed bool
}

// buildNodes reads the desired document into nodes, walking the owned set in
// step with it. A value that is an object but cannot be read member by member
// (it names a key twice) is a leaf: it is copied as written, as one value.
// notLeaf is set when an owned path names a non-empty object.
func buildNodes(members []jsonMember, set *ownedSet, parent []string, seq *int, notLeaf *bool) []*jsonNode {
	nodes := make([]*jsonNode, 0, len(members))
	for _, m := range members {
		n := &jsonNode{member: m, seq: *seq}
		*seq++
		n.path = append(slices.Clone(parent), m.name)
		below := set.child(m.name)
		if isObjectKind(m.value) {
			if inner, reason := readObject(m.value); reason == "" {
				if len(inner) == 0 {
					n.empty = true
				} else {
					n.children = buildNodes(inner, below, n.path, seq, notLeaf)
				}
			}
		}
		if n.children != nil {
			if below.owns() {
				*notLeaf = true
			}
			for _, c := range n.children {
				if c.claimed {
					n.claimed = true
					break
				}
			}
		} else {
			n.claimed = below.owns()
		}
		nodes = append(nodes, n)
	}
	return nodes
}

type noteRecord struct {
	seq  int
	note Note
}

// jsonMerger carries the notes of one merge.
type jsonMerger struct {
	notes []noteRecord
}

// note records one decision. A node's path is its own slice and a node yields at
// most one note, so notes never share a path.
func (m *jsonMerger) note(code NoteCode, n *jsonNode) {
	m.notes = append(m.notes, noteRecord{seq: n.seq, note: Note{Code: code, Path: n.path}})
}

// orderedNotes returns the notes in the order of the desired document.
func (m *jsonMerger) orderedNotes() []Note {
	if len(m.notes) == 0 {
		return nil
	}
	sort.SliceStable(m.notes, func(i, j int) bool { return m.notes[i].seq < m.notes[j].seq })
	out := make([]Note, len(m.notes))
	for i, r := range m.notes {
		out[i] = r.note
	}
	return out
}

// compose writes the merged object: every member found, in the order found,
// carrying the desired value where the desired document declares that key and
// the caller owns it, and then the members only the desired document declares
// and the caller owns.
func (m *jsonMerger) compose(declared []*jsonNode, found []jsonMember) []byte {
	byName := make(map[string]*jsonNode, len(declared))
	for _, n := range declared {
		byName[n.member.name] = n
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	write := func(key, value []byte) {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	present := make(map[string]struct{}, len(found))
	for _, f := range found {
		n, ok := byName[f.name]
		if !ok {
			write(f.key, f.value)
			continue
		}
		present[f.name] = struct{}{}
		if !n.claimed {
			write(f.key, f.value)
			if !sameJSON(n.member.value, f.value) {
				m.note(NoteUnownedKept, n)
			}
			continue
		}
		write(f.key, m.value(n, f.value))
	}
	for _, n := range declared {
		if _, ok := present[n.member.name]; ok {
			continue
		}
		if !n.claimed {
			m.note(NoteUnownedSkipped, n)
			continue
		}
		write(n.member.key, m.prune(n))
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// value composes one claimed member's declared value over the value found at
// that key: member by member when both are objects, and the declared value
// otherwise.
func (m *jsonMerger) value(n *jsonNode, found []byte) []byte {
	var members []jsonMember
	readable := false
	if isObjectKind(found) {
		var reason Reason
		members, reason = readObject(found)
		readable = reason == ""
	}
	switch {
	case n.children != nil && readable:
		return m.compose(n.children, members)
	case n.empty && readable:
		return found
	}
	if isObjectKind(n.member.value) || isObjectKind(found) {
		m.note(NoteTypeConflict, n)
	}
	return m.prune(n)
}

// prune writes a declared member whole, except that an object is written with
// only the members the caller owns, and the ancestors needed to reach them.
func (m *jsonMerger) prune(n *jsonNode) []byte {
	if n.children == nil {
		return n.member.value
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, c := range n.children {
		if !c.claimed {
			m.note(NoteUnownedSkipped, c)
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(c.member.key)
		buf.WriteByte(':')
		buf.Write(m.prune(c))
	}
	buf.WriteByte('}')
	return buf.Bytes()
}
