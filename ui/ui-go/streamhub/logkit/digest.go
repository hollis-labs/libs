package logkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"

	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// Digest is the duplicate-detection digest of an event: sha256 over Name and
// a canonical form of Data. Valid JSON is decoded and re-encoded with sorted
// keys and no insignificant whitespace (numbers keep their literal text), so
// a producer that re-marshals the same event on retry gets the original
// record, not a false [streamhub.ErrEventIDConflict]. Anything else is
// digested byte-exact. Every durable Log must use this one function: two
// implementations that disagree would disagree about what a duplicate is.
func Digest(e streamhub.Event) []byte {
	h := sha256.New()
	h.Write([]byte(e.Name))
	h.Write([]byte{0})
	h.Write(CanonicalJSON(e.Data))
	return h.Sum(nil)
}

// CanonicalJSON returns the canonical form of b if it is a single JSON value,
// and b itself otherwise.
func CanonicalJSON(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return b
	}
	if _, err := dec.Token(); err != io.EOF { // trailing data: not a single JSON value
		return b
	}
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}
