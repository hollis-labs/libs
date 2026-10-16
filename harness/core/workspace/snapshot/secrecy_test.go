package snapshot

import "testing"

func TestMandatorySecretNamesCannotBeReincluded(t *testing.T) {
	for _, p := range []string{".env", ".env.production", "sub/.ENV.local", ".codex/auth.json", "config/token.json", "keys/private.key", "cert.pem", ".git/objects/a", "../escape"} {
		if !SecretExcluded(p) {
			t.Fatalf("known secret scope accepted: %s", p)
		}
	}
	for _, p := range []string{"source.go", "src/token.go", "docs/configuration.md"} {
		if SecretExcluded(p) {
			t.Fatalf("ordinary source excluded: %s", p)
		}
	}
}
