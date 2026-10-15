package redact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestText_ReplacesExactOccurrences(t *testing.T) {
	got := Text(`bad token "sk-live-123456" (retry sk-live-123456)`, []string{"sk-live-123456"})
	if want := `bad token "[redacted]" (retry [redacted])`; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
}

// supervise.Redact also blanks a secret's edge fragments, because a stderr
// tail can be cut mid-value. Whole error text must not lose an ordinary
// word that merely ends with a secret's first letters.
func TestText_LeavesEdgeFragmentsOfWholeText(t *testing.T) {
	text := "messaging service not configured"
	if got := Text(text, []string{"default", "dsk_abcdef"}); got != text {
		t.Fatalf("Text = %q, want it unchanged", got)
	}
}

func TestText_LongestSecretFirst(t *testing.T) {
	got := Text("key=abcd-efgh-ijkl", []string{"abcd", "abcd-efgh-ijkl"})
	if got != "key=[redacted]" {
		t.Fatalf("Text = %q, want the longer secret replaced whole", got)
	}
}

func TestText_IgnoresShortValues(t *testing.T) {
	text := "exit 1 on retry 10"
	if got := Text(text, []string{"1", "on", "", "10"}); got != text {
		t.Fatalf("Text = %q, want short values ignored", got)
	}
}

func TestSet_RedactAndNilSafety(t *testing.T) {
	var nilSet *Set
	nilSet.Add("whatever-secret")
	if got := nilSet.Redact("whatever-secret"); got != "whatever-secret" {
		t.Fatalf("nil Set redacted: %q", got)
	}

	var s Set
	s.Add("token-one", "x")
	if got := s.Redact("a token-one and x"); got != "a [redacted] and x" {
		t.Fatalf("Redact = %q", got)
	}
}

func TestSet_RememberAddsResolvedValuesOnly(t *testing.T) {
	var s Set
	ok := s.Remember(func(context.Context) (string, error) { return "resolved-key-1", nil })
	fail := s.Remember(func(context.Context) (string, error) { return "never-used-key", errors.New("helper failed") })

	if v, err := ok(context.Background()); err != nil || v != "resolved-key-1" {
		t.Fatalf("remembered resolver = %q, %v", v, err)
	}
	if _, err := fail(context.Background()); err == nil {
		t.Fatal("want the wrapped error")
	}
	if got := s.Redact("resolved-key-1 never-used-key"); got != "[redacted] never-used-key" {
		t.Fatalf("Redact = %q, want only the successfully resolved value", got)
	}
	if s.Remember(nil) != nil {
		t.Fatal("Remember(nil) should stay nil so callers keep their no-key path")
	}
}

func TestSet_AddsEscapedForms(t *testing.T) {
	var s Set
	s.Add(`pa ss&"w/ord`)
	for _, text := range []string{
		`raw pa ss&"w/ord here`,
		`url pa+ss%26%22w%2Ford here`,
		`json pa ss&\"w/ord here`,
	} {
		got := s.Redact(text)
		if strings.Contains(got, "ss") && !strings.Contains(got, Marker) {
			t.Fatalf("Redact(%q) = %q, want a form scrubbed", text, got)
		}
		if strings.Contains(got, "w/ord") || strings.Contains(got, "w%2Ford") {
			t.Fatalf("Redact(%q) = %q leaked a form", text, got)
		}
	}
}

func TestSet_CountsValuesTooShortToProtect(t *testing.T) {
	var s Set
	s.Add("on", "1", "long-enough-value")
	if got := s.Unprotected(); got != 2 {
		t.Fatalf("Unprotected = %d, want 2", got)
	}
	var nilSet *Set
	if nilSet.Unprotected() != 0 {
		t.Fatal("nil Set must report 0")
	}
}

func TestSet_ErrorKeepsTheCauseReachable(t *testing.T) {
	var s Set
	s.Add("sk-live-123456")
	cause := errors.New("bad key sk-live-123456")
	got := s.Error(fmt.Errorf("call: %w", cause))
	if strings.Contains(got.Error(), "sk-live") {
		t.Fatalf("error text leaked: %v", got)
	}
	if !errors.Is(got, cause) {
		t.Fatal("errors.Is must still reach the cause")
	}
	clean := errors.New("nothing to scrub")
	if !errors.Is(s.Error(clean), clean) || s.Error(clean).Error() != clean.Error() {
		t.Fatal("an error with nothing to scrub must be returned as it was")
	}
	if s.Error(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}
