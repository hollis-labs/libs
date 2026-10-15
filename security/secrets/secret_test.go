package secrets_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	secrets "github.com/hollis-labs/libs/security/secrets"
	"github.com/hollis-labs/libs/security/secrets/secretstest"
)

const value = "sk-live-0123456789abcdef"

func TestSecretNeverPrintsItsValue(t *testing.T) {
	s := secrets.FromString(value)
	secretstest.AssertNoLeak(t, value, s, &s, secrets.Resolved{Secret: s, Ref: "keychain://a/b", Source: "keychain"},
		struct {
			Name string
			Key  secrets.Secret
		}{"n", s}, map[string]secrets.Secret{"k": s}, []secrets.Secret{s})
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		if got := fmt.Sprintf(verb, s); got != secrets.Redacted {
			t.Errorf("Sprintf(%s) = %q, want %q", verb, got, secrets.Redacted)
		}
	}
}

func TestSecretEncodesAsRedactedAndRevealsOnRequest(t *testing.T) {
	s := secrets.FromString(value)
	b, err := json.Marshal(s)
	if err != nil || string(b) != `"[redacted]"` {
		t.Fatalf("json = %s, %v", b, err)
	}
	if txt, _ := s.MarshalText(); string(txt) != secrets.Redacted {
		t.Fatalf("text = %q", txt)
	}
	if s.LogValue().String() != secrets.Redacted {
		t.Fatalf("slog value = %v", s.LogValue())
	}
	var sb strings.Builder
	slog.New(slog.NewTextHandler(&sb, nil)).Info("x", "k", s)
	if strings.Contains(sb.String(), value) {
		t.Fatalf("slog leaked: %s", sb.String())
	}
	if s.Reveal() != value || string(s.Bytes()) != value || s.Len() != len(value) || s.IsZero() {
		t.Fatal("Reveal/Bytes/Len/IsZero wrong")
	}
}

func TestSecretBytesIsACopyAndZeroWipes(t *testing.T) {
	src := []byte(value)
	s := secrets.FromBytes(src)
	src[0] = 'X'
	if s.Reveal() != value {
		t.Fatal("FromBytes must copy its input")
	}
	b := s.Bytes()
	b[0] = 'Y'
	if s.Reveal() != value {
		t.Fatal("Bytes must return a copy")
	}
	s.Zero()
	if !s.IsZero() || s.Reveal() != "" {
		t.Fatal("Zero must empty the secret")
	}
}
