package bootkey_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
)

func TestEncodeReadableExactIdentity(t *testing.T) {
	for _, tc := range []struct{ identity, slug string }{
		{"Agent-One", "agent-one"},
		{"msg://agent/Example_A", "msg-agent-example_a"},
		{"a/b", "a-b"}, {"a b", "a-b"}, {"a:b", "a-b"},
		{"a;b", "a-b"}, {"a\\b", "a-b"}, {"a\tb", "a-b"},
		{"--A---B--", "a-b"}, {"a.b_c-9", "a.b_c-9"},
		{"üñïçødé seed", "d-seed"}, {"..", ".."},
		{"/", ""}, {"   ", ""},
	} {
		t.Run(tc.identity, func(t *testing.T) {
			key, err := bootkey.Encode(tc.identity)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(tc.identity))
			want := bootkey.Version + "-"
			if tc.slug != "" {
				want += tc.slug + "-"
			}
			want += hex.EncodeToString(sum[:])
			if key != want {
				t.Fatalf("key = %q; want %q", key, want)
			}
			if err := bootkey.ValidateComponent(key); err != nil {
				t.Fatal(err)
			}
			again, err := bootkey.Encode(tc.identity)
			if err != nil || again != key {
				t.Fatalf("unstable encoding: %q, %v", again, err)
			}
		})
	}
}

func TestEncodeSeparatesEqualSlugsAndExactBytes(t *testing.T) {
	seen := map[string]string{}
	for _, identity := range []string{"a/b", "a b", "a:b", "a;b", "a\\b", "a\tb", "A/b", "a/b ", " a/b"} {
		key, err := bootkey.Encode(identity)
		if err != nil {
			t.Fatal(err)
		}
		if prior, exists := seen[key]; exists {
			t.Fatalf("%q collides with %q", identity, prior)
		}
		seen[key] = identity
	}
}

func TestEncodeDoesNotPassThroughGeneratedKey(t *testing.T) {
	// The legacy passthrough collided between an unsafe seed and its own
	// generated output supplied as a safe seed. Both must be hashed here.
	const identity = "a/b"
	sum := sha256.Sum256([]byte(identity))
	legacyOutput := "a-b-" + hex.EncodeToString(sum[:])
	key, err := bootkey.Encode(identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{legacyOutput, key} {
		encoded, err := bootkey.Encode(other)
		if err != nil {
			t.Fatal(err)
		}
		if encoded == key || encoded == other {
			t.Fatalf("passthrough collision for %q", other)
		}
	}
}

func TestEncodeBoundsLongIdentityWithoutLosingSuffix(t *testing.T) {
	prefix := strings.Repeat("A", 10000)
	first, err := bootkey.Encode(prefix + "x")
	if err != nil {
		t.Fatal(err)
	}
	second, err := bootkey.Encode(prefix + "y")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != bootkey.MaxKeyLength || len(second) != bootkey.MaxKeyLength {
		t.Fatal("output exceeds or misses bounded slug shape")
	}
	if first == second {
		t.Fatal("truncated digest input")
	}
	if !strings.HasPrefix(first, bootkey.Version+"-"+strings.Repeat("a", bootkey.MaxSlugLength)+"-") {
		t.Fatal("unexpected bounded slug")
	}
}

func TestEncodeRequiresIdentity(t *testing.T) {
	key, err := bootkey.Encode("")
	if key != "" || !errors.Is(err, bootkey.ErrEmptyIdentity) {
		t.Fatalf("Encode empty = %q, %v", key, err)
	}
}

func TestValidateComponent(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "../escape", "a\\b", ".prev-", ".prev-generation", "a\x00b", "a b", "a:b", "ü", strings.Repeat("a", bootkey.MaxKeyLength+1)} {
		if err := bootkey.ValidateComponent(bad); !errors.Is(err, bootkey.ErrInvalidComponent) {
			t.Errorf("accepted invalid component %q: %v", bad, err)
		}
	}
	for _, good := range []string{"planner", "A", "9x", "a.b_c-9", ".hidden", "current", strings.Repeat("a", bootkey.MaxKeyLength)} {
		if err := bootkey.ValidateComponent(good); err != nil {
			t.Errorf("refused %q: %v", good, err)
		}
	}
}
