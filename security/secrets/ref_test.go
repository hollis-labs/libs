package secrets_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	secrets "github.com/hollis-labs/libs/security/secrets"
)

func TestParseRef(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want secrets.Ref
	}{
		{"keychain", "keychain://openai/personal",
			secrets.Ref{Raw: "keychain://openai/personal", Scheme: "keychain", Authority: "openai", Path: []string{"personal"}}},
		{"helper", "helper://tether-apikey-helper/openai/default",
			secrets.Ref{Raw: "helper://tether-apikey-helper/openai/default", Scheme: "helper", Authority: "tether-apikey-helper", Path: []string{"openai", "default"}}},
		{"keyring alias", "keyring://svc/key",
			secrets.Ref{Raw: "keyring://svc/key", Scheme: "keyring", Authority: "svc", Path: []string{"key"}}},
		{"op with spaces", "op://Personal/My Item/password",
			secrets.Ref{Raw: "op://Personal/My Item/password", Scheme: "op", Authority: "Personal", Path: []string{"My Item", "password"}}},
		{"env", "env://OPENAI_API_KEY",
			secrets.Ref{Raw: "env://OPENAI_API_KEY", Scheme: "env", Authority: "OPENAI_API_KEY"}},
		{"file absolute", "file:///home/u/.tok",
			secrets.Ref{Raw: "file:///home/u/.tok", Scheme: "file", Path: []string{"/home/u/.tok"}}},
		{"file home", "file://~/.tok",
			secrets.Ref{Raw: "file://~/.tok", Scheme: "file", Path: []string{"~/.tok"}}},
		{"trims", "  keychain://a/b \n",
			secrets.Ref{Raw: "keychain://a/b", Scheme: "keychain", Authority: "a", Path: []string{"b"}}},
		{"future vault scheme, same grammar", "vault://secret/data/app#key",
			secrets.Ref{Raw: "vault://secret/data/app#key", Scheme: "vault", Authority: "secret", Path: []string{"data", "app#key"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := secrets.ParseRef(tc.raw)
			if err != nil {
				t.Fatalf("ParseRef(%q) = %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseRef(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseRefRejectsAndNeverEchoesItsInput(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"", "  ", "sk-live-0123456789abcdef", "no scheme/here", "://x/y", "Keychain://a/b",
		"keychain://", "keychain:///b", "keychain://a//b", "keychain://a/b/", "keychain://a/b\x00c",
	} {
		_, err := secrets.ParseRef(raw)
		if !errors.Is(err, secrets.ErrInvalid) {
			t.Errorf("ParseRef(%q) err = %v, want KindInvalid", raw, err)
			continue
		}
		if strings.TrimSpace(raw) != "" && strings.Contains(err.Error(), strings.TrimSpace(raw)) {
			t.Errorf("ParseRef(%q) error echoes its input: %v", raw, err)
		}
	}
}

func TestIsRef(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]bool{
		"keychain://a/b": true, "keyring://a/b": true, "helper://h/x": true, "file:///a": true,
		"op://v/i/f": true, "env://X": true, "https://example.com/x": false, "plain": false, "keychain://": false,
	} {
		if got := secrets.IsRef(raw); got != want {
			t.Errorf("IsRef(%q) = %v, want %v", raw, got, want)
		}
	}
}
