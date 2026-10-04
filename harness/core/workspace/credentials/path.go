package credentials

import (
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

var ErrUnsafePath = errors.New("credentials: unsafe relative path")

// ValidateRelPath implements the credential leaf's slash-relative resource
// rule without importing artifact or render packages. Sources may use Unicode;
// destination components are fixed provider names and additionally require ASCII.
func ValidateRelPath(rel string) error {
	if !utf8.ValidString(rel) || len(rel) > 4096 || strings.TrimSpace(rel) == "" || path.IsAbs(rel) || strings.ContainsAny(rel, "\\:{}\x00\r\n") || path.Clean(rel) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ErrUnsafePath
	}
	parts := strings.Split(rel, "/")
	if len(parts) > 64 {
		return ErrUnsafePath
	}
	for _, part := range parts {
		if len(part) > 255 {
			return ErrUnsafePath
		}
		for _, r := range part {
			if r < 32 || r == 127 || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
				return ErrUnsafePath
			}
		}
	}
	for _, name := range []string{".git", ".ssh", ".gnupg", ".aws", ".materialize"} {
		if strings.EqualFold(parts[0], name) {
			return ErrUnsafePath
		}
	}
	return nil
}
func ValidateDestination(rel string) error {
	if ValidateRelPath(rel) != nil {
		return ErrUnsafePath
	}
	for _, b := range []byte(rel) {
		if b >= 128 {
			return ErrUnsafePath
		}
	}
	return nil
}
