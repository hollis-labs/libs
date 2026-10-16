// Package keymerge merges the document an installer wants in a provider's
// configuration file into the document already there. Those files are written
// by the operator and by the provider as well, so the question is never "what
// should the file say" but "which keys are the installer's". The package
// answers it key by key, for JSON settings documents and for TOML configuration
// files, and reproduces the behavior of the installer the archived installed
// seeds were captured from (Cairn, github.com/hollis-labs/cairn), byte for byte
// where that installer could express the case.
//
// It is pure: bytes in, bytes out. It reads no file, environment variable or
// clock, keeps no state, starts nothing, and imports no other package of this
// module. Inputs are never modified and results share no memory with them.
//
// # Owned keys
//
// The caller passes the desired document (what an install would write into an
// empty file) and the owned key paths: the exact paths of the leaves the
// installer owns. A path is a list of components, never a dotted string, so a
// key that contains a dot is one component. A leaf is anything that is not a
// non-empty object or table: a scalar, an array (including an array of tables),
// null, or an empty object. An owned path that names a non-empty object or
// table in the desired document is refused with an *Error whose code is
// CodeInvalidOwnedPaths, as is a path with no components; an owned path that
// leads nowhere in the desired document is not an error and does nothing.
//
// A desired key is written only where the caller owns it: it is an owned leaf,
// or an object or table with an owned leaf somewhere beneath it. That is the
// whole write set. Everything else is left alone:
//
//   - A key the document found holds and the desired document does not declare
//     stands, wherever it sits, as the bytes found.
//   - A key the desired document declares and the caller does not own, and the
//     document found holds, stands as found, whatever the desired value is. The
//     desired document may carry such keys as slots already merged from an
//     operator's document; they are never written over the operator's.
//   - A key the desired document declares and the caller does not own, and the
//     document found lacks, is not added.
//   - An owned path the desired document does not declare removes nothing: the
//     installer cannot remove a nested key it has stopped declaring, because
//     nothing distinguishes it from a key the operator wrote. That is a stated
//     limit of the original behavior and it is kept.
//
// When the caller owns every leaf the desired document declares, this is the
// original rule exactly: every declared key carries the declared value at every
// depth, two objects merge member by member, and anything else at a declared
// key (an array, a scalar, null) takes the declared value whole. An owned set
// narrower than that is a deviation from the original behavior, and only a
// narrower set can show it, because the original had no way to say "declared
// but not owned". Where an owned key holds an object or table on one side and
// something that cannot be merged with it on the other, the declared side is
// written, and a Note says so.
//
// # JSON
//
// MergeJSON needs both documents to be exactly one readable JSON object:
// nothing else in front of it or behind it, and no key twice in the top-level
// object (the standard decoder resolves a duplicate silently by dropping a
// member, and quietly doing that to an operator's file is exactly what the
// installer refuses to do). Nesting deeper than the standard decoder allows is
// not readable either.
//
// Nothing is re-encoded. Keys and values are copied as the raw bytes of
// whichever document they came from, so a string holding "<" stays as it was
// written, a number keeps its spelling, and two spellings of one key (a and
// its \u0061 escape) name the same member and are matched on the decoded name,
// the spelling on disk being the one written back. The order is the order found,
// then the keys only the desired document declares, in its order. The result
// is laid out one element per line at two spaces per level and ends in a
// newline, which moves white space between tokens and changes nothing else.
// A document at rest, already laid out this way, whose declared values agree
// is returned byte for byte; the same document laid out any other way comes
// back in this layout.
//
// Only the top-level object is refused for duplicates. A member the desired
// document declares and the caller owns, whose object in the found document
// names a key twice, cannot be merged member by member: the declared value is
// written, with a Note of type_conflict. (A found member the caller does not own
// stands as it is, duplicates and all.) A declared member whose own object names
// a key twice cannot be walked either and is copied as written.
//
// # Documents that cannot be read
//
// A JSON document that is not one readable object is an Outcome, not an error,
// and the desired document comes back untouched, with the Reason that says why
// (empty, not_json, not_object, trailing_content, duplicate_key). For the found
// document (OutcomeExistingUnreadable) that is what the installer does and is
// kept on purpose: the install overwrites a document it cannot read, and the
// comparison reports it as different. The caller decides whether that
// overwrite keeps a recovery copy or refuses; this package only reports. The
// untouched bytes are the whole desired document, slots the caller does not own
// included, and are neither laid out nor given a newline. A desired document
// that is not readable (OutcomeDesiredUnreadable) comes back the same way, and
// a caller must look at the Outcome before writing a Document.
//
// An empty or white-space-only JSON file is the first of those, so it is
// replaced by the desired bytes as given. An empty TOML file is different: it
// is the empty table, and merges to the desired document as the TOML encoder
// writes it. A caller treats an absent file as its own case.
//
// # TOML
//
// MergeTOML parses both documents, merges the trees with the same rule, and
// writes the result with the TOML encoder of github.com/pelletier/go-toml/v2,
// the library and version the original used. That is not byte preserving: keys
// come out sorted, plain values before tables, comments are gone, strings come
// out in the encoder's quoting, an inline table and a table are the same table,
// and numbers and dates are written as the encoder spells them. The order of a
// found TOML document is therefore not kept, unlike JSON. A table meeting a
// value that is not a table at an owned key resolves to the declared side.
//
// A TOML document that does not parse is an *Error, with a code that tells the
// found document (CodeExistingTOMLInvalid) from the desired one
// (CodeDesiredTOMLInvalid), which is read first. The error carries the line and
// column the parser reported and never any text of the document, and it does
// not wrap the parser's own error, which can echo the document.
//
// The encoder cannot fail on a tree its own decoder produced, which is all this
// package ever hands it. The code keeps the original fallback for that case,
// the desired document as given, and it is unreachable through the API.
//
// # Comparison
//
// CompareJSON and CompareTOML answer a drift check: is the document found what
// a merge would write? For JSON both sides are laid out the same way first, so
// layout and the position of a declared key are forgiven and a changed value, a
// removed key, a respelled number or a duplicate key is not. For TOML both
// sides are normalized with the encoder (NormalizeTOML), which forgives what
// the encoder moves. A key the desired document does not declare, and a
// declared key the caller does not own, are not compared. A document that
// cannot be read is a verdict, not an error. An unreadable found JSON document
// is normally a mismatch, but can match when its only invalidity is outer white
// space that bytes.TrimSpace removes before layout even though JSON rejects it;
// Outcome and Reason still report the unreadable document. An unreadable TOML
// document never matches.
//
// # Notes
//
// A merge reports its decisions about declared keys as Notes: a code and a key
// path, never a value. unowned_kept says an unowned declared key was found with
// a different value and the found value stood; unowned_skipped says one was
// absent and nothing was written; type_conflict is described above. There is
// at most one note per declared key, so the count is bounded by the desired
// document and never by the document found. JSON notes follow the order of the
// desired document; a decoded TOML tree has no document order, so TOML notes
// are ordered by key path. Key paths can still be sensitive in some documents,
// for example where keys are filesystem paths: callers must not publish them.
package keymerge
