package snapshot

import (
	"io/fs"
	"path"
	"strings"
)

// SecretExcluded identifies known credential metadata, never secret contents.
// It is mandatory and cannot be undone by an opt-in. This is not a detector
// for arbitrary secrets pasted into otherwise eligible source files.
func SecretExcluded(relativePath string) bool {
	if !fs.ValidPath(relativePath) {
		return true
	}
	for _, name := range strings.Split(relativePath, "/") {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, ".env") {
			return true
		}
		switch lower {
		case ".git", ".claude", ".codex", ".gemini", ".config", ".ssh", ".aws", ".azure", ".gnupg",
			"auth.json", "credentials", "credentials.json", "credentials.yaml", "credentials.yml",
			"token", "tokens", "token.json", "tokens.json", "token.txt", "operator.token",
			"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
			return true
		}
		switch path.Ext(lower) {
		case ".pem", ".key", ".p12", ".pfx", ".token":
			return true
		}
	}
	return false
}
