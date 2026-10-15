package keymerge

import (
	"bytes"
	"errors"
	"maps"
	"reflect"
	"slices"

	"github.com/pelletier/go-toml/v2"
)

// MergeTOML returns the TOML document an install writes where one already
// exists. Both documents are parsed, merged as trees of tables, and written
// back by the TOML encoder; see the package documentation for what that means
// for comments, quoting and order.
//
// The rule is MergeJSON's, on tables: a declared key the caller owns carries
// the declared value, a table merges key by key, a key only the document found
// holds stands, and a declared key the caller does not own is never overwritten
// and never added. Where a table meets a value that is not a table at an owned
// key, the declared side is written.
//
// A document that does not parse is a typed error, and the desired document is
// read, and reported, first. A document with nothing in it, or only comments,
// is the empty table.
func MergeTOML(desired, existing []byte, owned []KeyPath) (TOMLMerge, error) {
	set, err := newOwnedSet(owned)
	if err != nil {
		return TOMLMerge{}, err
	}
	var want map[string]any
	if err := toml.Unmarshal(desired, &want); err != nil {
		return TOMLMerge{}, tomlError(CodeDesiredTOMLInvalid, err)
	}
	var notLeaf bool
	declared := buildTOMLNodes(want, set, nil, &notLeaf)
	if notLeaf {
		return TOMLMerge{}, errNotLeaf()
	}
	var found map[string]any
	if err := toml.Unmarshal(existing, &found); err != nil {
		return TOMLMerge{}, tomlError(CodeExistingTOMLInvalid, err)
	}
	var m tomlMerger
	document := encodeTree(m.compose(declared, found), desired)
	return TOMLMerge{Document: document, Notes: m.notes}, nil
}

// NormalizeTOML returns raw as the TOML encoder writes the document it parses
// to, and true. A document that does not parse comes back as a copy of the
// bytes given, and false. Two documents that differ only in what the encoder
// moves (comments, quoting, order, table spelling) normalize to the same bytes.
func NormalizeTOML(raw []byte) ([]byte, bool) {
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return bytes.Clone(raw), false
	}
	return normalizeTree(doc, raw)
}

// encodeTree writes a merged tree. The encoder cannot fail on a tree its own
// decoder produced, which is all a merge builds, so the fallback is not
// reachable through the API; it keeps the installer's, which is the desired
// document as given, in a copy.
func encodeTree(tree any, desired []byte) []byte {
	out, ok := marshalTree(tree)
	if !ok {
		return bytes.Clone(desired)
	}
	return out
}

// normalizeTree writes a parsed document; when the encoder refuses (also not
// reachable through the API) the bytes the document came from come back, in a
// copy, with false.
func normalizeTree(tree any, raw []byte) ([]byte, bool) {
	out, ok := marshalTree(tree)
	if !ok {
		return bytes.Clone(raw), false
	}
	return out, true
}

// marshalTree encodes a tree. It reports false when the encoder refuses, which
// it does for a tree holding a value its own decoder never produces (a nil
// interface, an unsupported type, a map that is not keyed by strings).
func marshalTree(tree any) ([]byte, bool) {
	out, err := toml.Marshal(tree)
	if err != nil {
		return nil, false
	}
	return out, true
}

// tomlError builds the typed error for a document that does not parse. It does
// not wrap the parser's error: that text can echo the document. The position is
// the parser's, as numbers.
func tomlError(code Code, err error) *Error {
	e := &Error{Code: code, Reason: ReasonNotTOML}
	var decode *toml.DecodeError
	if errors.As(err, &decode) {
		e.Line, e.Column = decode.Position()
	}
	return e
}

// tomlNode is one key of the desired document.
type tomlNode struct {
	name string
	path []string
	// value is the decoded value: a map for a table, and for everything else
	// (including an array of tables) a leaf.
	value any
	// children is set when the value is a non-empty table, in sorted key order.
	children []*tomlNode
	// empty is set when the value is a table with no keys.
	empty bool
	// claimed is true when the merge writes this key: it is an owned leaf, or a
	// table with a claimed key beneath it.
	claimed bool
}

// buildTOMLNodes reads the desired tree into nodes in sorted key order, walking
// the owned set in step with it. notLeaf is set when an owned path names a
// non-empty table.
func buildTOMLNodes(table map[string]any, set *ownedSet, parent []string, notLeaf *bool) []*tomlNode {
	nodes := make([]*tomlNode, 0, len(table))
	for _, name := range slices.Sorted(maps.Keys(table)) {
		value := table[name]
		n := &tomlNode{name: name, value: value, path: append(slices.Clone(parent), name)}
		below := set.child(name)
		if inner, ok := value.(map[string]any); ok && len(inner) > 0 {
			n.children = buildTOMLNodes(inner, below, n.path, notLeaf)
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
			n.empty = ok
			n.claimed = below.owns()
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// tomlMerger carries the notes of one merge. Nodes are visited in sorted key
// order, parents before the keys beneath them, so notes come out ordered by key
// path.
type tomlMerger struct {
	notes []Note
}

// note records one decision. A node's path is its own slice and a node yields at
// most one note, so notes never share a path.
func (m *tomlMerger) note(code NoteCode, n *tomlNode) {
	m.notes = append(m.notes, Note{Code: code, Path: n.path})
}

// compose merges the declared tables over the table found. The table found is
// not modified.
func (m *tomlMerger) compose(declared []*tomlNode, found map[string]any) map[string]any {
	out := make(map[string]any, len(found)+len(declared))
	maps.Copy(out, found)
	for _, n := range declared {
		current, present := found[n.name]
		switch {
		case !present && !n.claimed:
			m.note(NoteUnownedSkipped, n)
		case !present:
			out[n.name] = m.prune(n)
		case !n.claimed:
			if !reflect.DeepEqual(n.value, current) {
				m.note(NoteUnownedKept, n)
			}
		default:
			out[n.name] = m.value(n, current)
		}
	}
	return out
}

// value composes one claimed key's declared value over the value found there:
// key by key when both are tables, and the declared value otherwise.
func (m *tomlMerger) value(n *tomlNode, current any) any {
	table, isTable := current.(map[string]any)
	switch {
	case n.children != nil && isTable:
		return m.compose(n.children, table)
	case n.empty && isTable:
		return table
	}
	if n.children != nil || n.empty || isTable {
		m.note(NoteTypeConflict, n)
	}
	return m.prune(n)
}

// prune writes a declared key whole, except that a table is written with only
// the keys the caller owns, and the tables needed to reach them.
func (m *tomlMerger) prune(n *tomlNode) any {
	if n.children == nil {
		return n.value
	}
	out := make(map[string]any, len(n.children))
	for _, c := range n.children {
		if !c.claimed {
			m.note(NoteUnownedSkipped, c)
			continue
		}
		out[c.name] = m.prune(c)
	}
	return out
}
