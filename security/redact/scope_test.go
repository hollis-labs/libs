package redact_test

import (
	"encoding/base64"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/security/redact"
)

const knownVal = "Tq9!zR+7/mK2&pL" // awkward on purpose: needs escaping in URLs and JSON

func TestScopeRedactsAKnownValueWithNoLabelAndInItsEncodedForms(t *testing.T) {
	s := redact.NewScope()
	if !s.Add("DB_PASS", knownVal) {
		t.Fatal("a value this long must be protected")
	}
	r := mustNew(t, redact.WithScope(s))
	forms := []string{
		knownVal, url.QueryEscape(knownVal), url.PathEscape(knownVal),
		strings.ReplaceAll(knownVal, "&", "&amp;"), strings.ReplaceAll(knownVal, "&", `&`),
		base64.StdEncoding.EncodeToString([]byte(knownVal)), base64.RawURLEncoding.EncodeToString([]byte(knownVal)),
	}
	for _, f := range forms {
		in := "prefix " + f + " suffix"
		out := r.Text(in)
		if strings.Contains(out, f) || !strings.HasPrefix(out, "prefix ") || !strings.HasSuffix(out, " suffix") {
			t.Errorf("form %q not handled: %q", f, out)
		}
	}
	if got := r.Text("an ordinary sentence"); got != "an ordinary sentence" {
		t.Error(got)
	}
}

func TestScopeMinimumLengthProtectsWordsAndReportsWhatItCannotCover(t *testing.T) {
	s := redact.NewScope()
	for name, v := range map[string]string{"FLAG": "on", "SHORT": "pass", "NUM": "1"} {
		if s.Add(name, v) {
			t.Errorf("%s=%q is too short to protect", name, v)
		}
	}
	if got := mustNew(t, redact.WithScope(s)).Text("the passport and a tested pass on 1 path"); got != "the passport and a tested pass on 1 path" {
		t.Errorf("short values mangled ordinary words: %q", got)
	}
	un := s.Unprotected()
	if len(un) != 3 {
		t.Errorf("Unprotected = %v", un)
	}
	for _, n := range un {
		if strings.ContainsAny(n, " ") || n == "on" || n == "pass" {
			t.Errorf("Unprotected must list names, never values: %q", n)
		}
	}
	// the minimum can be lowered, but never below the floor
	low := redact.NewScope(redact.WithMinLen(1))
	if low.Add("x", "abc") || !low.Add("y", "abcd") {
		t.Error("the floor is 4")
	}
}

func TestScopeLongestFirstSoNestedSecretsAreNotLeftHalfScrubbed(t *testing.T) {
	s := redact.NewScope()
	s.Add("inner", "abcd1234")
	s.Add("outer", "abcd1234efgh5678")
	out := mustNew(t, redact.WithScope(s)).Text("x abcd1234efgh5678 y")
	if out != "x [REDACTED] y" {
		t.Errorf("%q", out)
	}
}

func TestScopeGrowsAfterTheRedactorIsBuilt(t *testing.T) {
	s := redact.NewScope()
	r := mustNew(t, redact.WithScope(s))
	in := "value is " + knownVal
	if r.Text(in) != in {
		t.Fatal("nothing is known yet")
	}
	s.Add("late", knownVal)
	if strings.Contains(r.Text(in), knownVal) {
		t.Error("a value learned later must be covered")
	}
}

func TestScopeIsSafeForConcurrentUse(t *testing.T) {
	s := redact.NewScope()
	r := mustNew(t, redact.WithScope(s))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); s.Add("n", strings.Repeat(string(rune('a'+i)), 12)) }(i)
		go func() { defer wg.Done(); _ = r.Text("aaaaaaaaaaaa bbbbbbbbbbbb token=Zz9-synthetic-1") }()
	}
	wg.Wait()
}

func TestTailBlanksCutOffFragmentsOfKnownValues(t *testing.T) {
	s := redact.NewScope()
	s.Add("tok", "SYNTHSECRET-0123456789")
	r := mustNew(t, redact.WithScope(s))
	if got := r.Tail("ordinary output SYNTHSECRET-0123"); got != "ordinary output [REDACTED]" {
		t.Errorf("end fragment: %q", got)
	}
	if got := r.Tail("456789 and then ordinary output"); got != "[REDACTED] and then ordinary output" {
		t.Errorf("start fragment: %q", got)
	}
	// whole text is exact and untouched elsewhere
	if got := r.Tail("nothing here"); got != "nothing here" {
		t.Errorf("%q", got)
	}
	// short coincidences are not fragments
	if got := r.Tail("ordinary SYNTH"); got != "ordinary SYNTH" {
		t.Errorf("%q", got)
	}
}
