package federation

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestFingerprintIsTheSHA256OfTheDER(t *testing.T) {
	c := validIdentity(t)
	sum := sha256.Sum256(c.Certificate[0])
	if got := Fingerprint(c.Leaf); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("Fingerprint = %s", got)
	}
}

func TestNormalizeFingerprintAcceptsOpensslAndBareForms(t *testing.T) {
	bare := strings.Repeat("ab12", 16)
	colon := strings.ToUpper(strings.Join(func() []string {
		var p []string
		for i := 0; i < len(bare); i += 2 {
			p = append(p, bare[i:i+2])
		}
		return p
	}(), ":"))
	for name, in := range map[string]string{"bare": bare, "colons upper": colon, "spaced": "  " + bare[:32] + " \n" + bare[32:], "mixed case": strings.ToUpper(bare)} {
		if got := NormalizeFingerprint(in); got != bare {
			t.Errorf("%s: %q", name, got)
		}
		if got, err := ValidateFingerprint(in); err != nil || got != bare {
			t.Errorf("%s: Validate = %q, %v", name, got, err)
		}
	}
	if NormalizeFingerprint(NormalizeFingerprint(colon)) != bare {
		t.Error("Normalize must be idempotent")
	}
}

func TestValidateFingerprintRejectsWhatIsNotASHA256(t *testing.T) {
	for name, in := range map[string]string{
		"empty": "", "short": strings.Repeat("a", 63), "long": strings.Repeat("a", 65),
		"not hex": strings.Repeat("g", 64), "sha1 length": strings.Repeat("a", 40),
	} {
		if _, err := ValidateFingerprint(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
