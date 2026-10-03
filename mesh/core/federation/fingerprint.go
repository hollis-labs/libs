package federation

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strings"
)

// fingerprintHexLen is the hex length of a SHA-256 digest.
const fingerprintHexLen = sha256.Size * 2

// Fingerprint returns the lower-case hex SHA-256 of a certificate's DER
// encoding: the value a peer is pinned by. It is computed over cert.Raw, which
// is exactly the bytes presented on the wire, so a pin computed here matches the
// one checked at the handshake.
func Fingerprint(cert *x509.Certificate) string {
	return fingerprintBytes(cert.Raw)
}

func fingerprintBytes(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint canonicalizes an operator-supplied fingerprint so the
// colon-separated form `openssl x509 -fingerprint` prints and bare hex compare
// equal: ':' and whitespace are dropped and the result is lower-cased.
func NormalizeFingerprint(s string) string {
	s = strings.ReplaceAll(s, ":", "")
	s = strings.Join(strings.Fields(s), "")
	return strings.ToLower(s)
}

// ValidateFingerprint normalizes s and checks it is a well-formed SHA-256
// fingerprint (64 hex characters). It returns the normalized form.
func ValidateFingerprint(s string) (string, error) {
	n := NormalizeFingerprint(s)
	if len(n) != fingerprintHexLen {
		return "", fmt.Errorf("fingerprint %q: want %d hex characters (SHA-256), got %d", s, fingerprintHexLen, len(n))
	}
	if _, err := hex.DecodeString(n); err != nil {
		return "", fmt.Errorf("fingerprint %q: not valid hex: %w", s, err)
	}
	return n, nil
}
