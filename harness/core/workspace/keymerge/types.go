package keymerge

// KeyPath names one key by its components from the document root. Components
// are separate strings, never a dotted string, so a key that contains a dot is
// one component. A component may be empty: "" is a legal key in both formats.
type KeyPath []string

// Outcome says what a merge did with the documents it was given.
type Outcome string

const (
	// OutcomeMerged: both documents were readable and the result is the merge.
	OutcomeMerged Outcome = "merged"
	// OutcomeExistingUnreadable: the document found on disk is not one readable
	// object. The result is the desired document, untouched, which is what an
	// install would write over it.
	OutcomeExistingUnreadable Outcome = "existing_unreadable"
	// OutcomeDesiredUnreadable: the desired document is not one readable
	// object. The result is that document, untouched.
	OutcomeDesiredUnreadable Outcome = "desired_unreadable"
)

// Reason says why a document was not readable, or why an input was refused. It
// never carries document content.
type Reason string

const (
	// ReasonEmpty: nothing but JSON white space, or no bytes at all.
	ReasonEmpty Reason = "empty"
	// ReasonNotJSON: the first value is malformed JSON.
	ReasonNotJSON Reason = "not_json"
	// ReasonNotObject: valid JSON whose first value is not an object.
	ReasonNotObject Reason = "not_object"
	// ReasonTrailingContent: one object, followed by anything but white space.
	ReasonTrailingContent Reason = "trailing_content"
	// ReasonDuplicateKey: the top-level object names one key twice.
	ReasonDuplicateKey Reason = "duplicate_key"
	// ReasonNotTOML: the document does not parse as TOML.
	ReasonNotTOML Reason = "not_toml"
	// ReasonEmptyPath: an owned key path has no components.
	ReasonEmptyPath Reason = "empty_path"
	// ReasonNotLeaf: an owned key path names a non-empty object or table in the
	// desired document. Owned paths name leaves.
	ReasonNotLeaf Reason = "not_leaf"
)

// NoteCode names a decision the merge made about one declared key.
type NoteCode string

const (
	// NoteUnownedKept: the desired document declares a key the caller does not
	// own, the document found holds a different value there, and the found
	// value stood.
	NoteUnownedKept NoteCode = "unowned_kept"
	// NoteUnownedSkipped: the desired document declares a key the caller does
	// not own, the document found has nothing there, and nothing was written.
	NoteUnownedSkipped NoteCode = "unowned_skipped"
	// NoteTypeConflict: a key the caller owns holds an object or table on one
	// side and something that could not be merged with it on the other. The
	// desired side was written.
	NoteTypeConflict NoteCode = "type_conflict"
)

// Note is one decision, as a code and a key path. It never carries a value.
// Key paths can still be sensitive in some documents (for example keys that are
// filesystem paths): do not publish them.
type Note struct {
	Code NoteCode
	Path KeyPath
}

// JSONMerge is the result of MergeJSON.
type JSONMerge struct {
	// Document is what an install writes. It is a fresh slice that shares no
	// memory with an input.
	Document []byte
	Outcome  Outcome
	// Reason is empty when Outcome is OutcomeMerged.
	Reason Reason
	// Notes are the merge's decisions about declared keys, in the document
	// order of the desired document. At most one note per declared key.
	Notes []Note
}

// TOMLMerge is the result of MergeTOML.
type TOMLMerge struct {
	// Document is what an install writes: a fresh slice.
	Document []byte
	// Notes are the merge's decisions about declared keys, ordered by key path
	// (a decoded TOML tree has no document order). At most one note per
	// declared key.
	Notes []Note
}

// Comparison is the result of CompareJSON and CompareTOML: whether the document
// found is what a merge would write, under the installer's rules.
type Comparison struct {
	// Match is true when the document found equals what the merge would write
	// after both are normalized the way the installer normalizes them.
	Match bool
	// Outcome and Reason are those of the merge the comparison was made
	// against; they tell a caller why a document that could not be merged did
	// not match.
	Outcome Outcome
	Reason  Reason
	// ExistingLen is the number of bytes found, and WriteLen the number a merge
	// would write; both are raw counts, before normalization.
	ExistingLen int
	WriteLen    int
}

// Code classifies an Error.
type Code string

const (
	// CodeExistingTOMLInvalid: the TOML document found cannot be parsed, so it
	// cannot be merged without guessing which bytes to preserve.
	CodeExistingTOMLInvalid Code = "existing_toml_invalid"
	// CodeDesiredTOMLInvalid: the desired TOML document cannot be parsed. It is
	// a different failure from the one above and is never confused with it.
	CodeDesiredTOMLInvalid Code = "desired_toml_invalid"
	// CodeInvalidOwnedPaths: the owned key paths are not usable. See Reason.
	CodeInvalidOwnedPaths Code = "invalid_owned_paths"
)

// Error is the typed error of this package. It carries a code, a reason and,
// for a TOML syntax error, the line and column the parser reported. It never
// carries document content, and it does not wrap the parser's own error
// because that text can echo the document. Match one with
// errors.Is(err, &Error{Code: ...}), which compares codes.
type Error struct {
	Code   Code
	Reason Reason
	// Line and Column are 1-based positions in the document that failed to
	// parse; zero when not applicable.
	Line   int
	Column int
}

// Error implements error. The text is fixed per code and reason.
func (e *Error) Error() string {
	text := "keymerge: " + string(e.Code)
	if e.Reason != "" {
		text += ": " + string(e.Reason)
	}
	return text
}

// Is reports whether target is an *Error with the same code.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other != nil && other.Code == e.Code
}
