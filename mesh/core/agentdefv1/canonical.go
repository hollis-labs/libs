package agentdef

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// DigestPrefix is the algorithm tag on every digest and pinned hash this
// package produces.
const DigestPrefix = "sha256:"

// Canonical returns the deterministic byte form fed to Digest: JSON with list
// order preserved (not sorted), Metadata keys sorted, Body trimmed and
// LF-normalized, and no HTML escaping. SourceRef and Layer are excluded.
func Canonical(d *Definition) ([]byte, error) {
	if d == nil {
		return nil, errors.New("agentdef: canonical of nil definition")
	}
	c := *d
	c.Body = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(d.Body, "\r\n", "\n"), "\r", "\n"))

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&c); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Digest returns "sha256:<hex>" over Canonical(d).
func Digest(d *Definition) (string, error) {
	b, err := Canonical(d)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return DigestPrefix + hex.EncodeToString(sum[:])
}
