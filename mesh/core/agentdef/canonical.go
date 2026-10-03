package agentdef

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Canonical returns the deterministic semantic payload. It excludes definition
// identity, revision labels, presentation and provenance. Ordered lists remain
// ordered, JSON map keys are sorted, and instructions use trimmed LF text.
// All extensions are pinned, even unsupported optional ones. Permission profile
// names are semantic; host binding-table behavior is external. The caller must
// Validate with its negotiation options before accepting or launching content;
// canonicalization itself intentionally needs no extension handlers.
func Canonical(d *Definition) ([]byte, error) {
	if d == nil {
		return nil, parseError("required", "definition is nil")
	}
	payload := struct {
		SchemaVersion  string               `json:"schema_version"`
		Behavior       Behavior             `json:"behavior"`
		Instructions   string               `json:"instructions"`
		Capabilities   []Capability         `json:"capabilities,omitempty"`
		Requirements   Requirements         `json:"requirements"`
		HarnessProfile HarnessProfile       `json:"harness_profile"`
		Continuity     Continuity           `json:"continuity"`
		Extensions     map[string]Extension `json:"extensions,omitempty"`
	}{d.SchemaVersion, d.Behavior, strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(d.Body, "\r\n", "\n"), "\r", "\n")), d.Capabilities, d.Requirements, d.HarnessProfile, d.Continuity, d.Extensions}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Digest is the SHA-256 semantic revision content identifier. It is not an
// enrollment identity or a mutable revision label.
func Digest(d *Definition) (string, error) {
	b, err := Canonical(d)
	if err != nil {
		return "", err
	}
	return ArtifactDigest(b), nil
}

// ArtifactDigest hashes exact authored file bytes, including formatting and
// provenance. It is deliberately separate from the semantic revision digest.
func ArtifactDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
