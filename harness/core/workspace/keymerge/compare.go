package keymerge

import (
	"bytes"
	"errors"
)

// CompareJSON reports whether a JSON document found on disk is what MergeJSON
// would write for the desired document, under the installer's rules: both are
// laid out the same way first, so a document laid out differently, or with a
// declared key moved, is the same document. Outer characters that
// bytes.TrimSpace accepts as white space are forgiven by that layout step even
// when JSON itself rejects them. A key the desired document does not declare is
// not compared, because a merge would not touch it, and neither is a declared
// key the caller does not own.
//
// Against a readable desired document, a found document that cannot be read is
// normally a mismatch. It can match when the unreadability is only outer white
// space removed by bytes.TrimSpace before layout. Outcome and Reason still say
// that the found document was unreadable, so callers that require readability
// must inspect Outcome. The error is for owned paths that cannot be honored.
func CompareJSON(desired, existing []byte, owned []KeyPath) (Comparison, error) {
	merged, err := MergeJSON(desired, existing, owned)
	if err != nil {
		return Comparison{}, err
	}
	return Comparison{
		Match:       bytes.Equal(layoutJSON(existing), layoutJSON(merged.Document)),
		Outcome:     merged.Outcome,
		Reason:      merged.Reason,
		ExistingLen: len(existing),
		WriteLen:    len(merged.Document),
	}, nil
}

// CompareTOML reports whether a TOML document found on disk is what MergeTOML
// would write for the desired document: both sides are normalized the way the
// TOML encoder writes them, so what the encoder moves (comments, quoting, order,
// table spelling) is forgiven and nothing else is.
//
// A document that does not parse, on either side, never matches, and is a
// verdict here rather than an error; Outcome and Reason say which. The error is
// for owned paths that cannot be honored.
func CompareTOML(desired, existing []byte, owned []KeyPath) (Comparison, error) {
	merged, err := MergeTOML(desired, existing, owned)
	if err != nil {
		var typed *Error
		if !errors.As(err, &typed) {
			return Comparison{}, err
		}
		outcome := OutcomeExistingUnreadable
		switch typed.Code {
		case CodeExistingTOMLInvalid:
		case CodeDesiredTOMLInvalid:
			outcome = OutcomeDesiredUnreadable
		default:
			return Comparison{}, err
		}
		return Comparison{Outcome: outcome, Reason: typed.Reason, ExistingLen: len(existing)}, nil
	}
	found, _ := NormalizeTOML(existing)
	want, _ := NormalizeTOML(merged.Document)
	return Comparison{
		Match:       bytes.Equal(found, want),
		Outcome:     OutcomeMerged,
		ExistingLen: len(existing),
		WriteLen:    len(merged.Document),
	}, nil
}
