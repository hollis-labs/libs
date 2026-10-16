package redact_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hollis-labs/libs/security/redact"
)

// FuzzText holds the engine to its invariants on arbitrary input: it never panics,
// redacting twice equals redacting once, findings are in range and ordered, valid UTF-8
// stays valid, and a line that held nothing sensitive is unchanged.
//
//	go test -fuzz=FuzzText -fuzztime=2m ./security/redact
func FuzzText(f *testing.F) {
	for _, s := range []string{
		"", "plain text", "password=hunter2", `{"password":"x","a":1}`, "Authorization: Bearer abc.def.ghi", "Bearer JWT only",
		"-----BEGIN PRIVATE KEY-----\nabc", "https://u:p@h/x?token=t&a=b", "--token abc", "sshpass -p x ssh h", "Cookie: a=b; c=d",
		"\u200bpass\u200bword=x", "ｐａｓｓｗｏｒｄ=x", "\xff\xfe=\xff", "a=b=c=d", `\"password\":\"x\"`, strings.Repeat("password=", 50),
		"aws configure set aws_secret_access_key abc", "curl -u a:b http://h", "key: value\nsecret: s3\n",
	} {
		f.Add(s)
	}
	r, err := redact.New(redact.WithHeuristics())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 8192 {
			t.Skip()
		}
		out := r.Text(s)
		if again := r.Text(out); again != out {
			t.Fatalf("not idempotent:\n in   %q\n once %q\n twice %q", s, out, again)
		}
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("valid UTF-8 became invalid: %q -> %q", s, out)
		}
		prev := 0
		for _, f := range r.Scan(s) {
			if f.Start < prev || f.End > len(s) || f.Start >= f.End {
				t.Fatalf("bad finding %+v for %q", f, s)
			}
			prev = f.End
		}
		if !strings.Contains(s, "=") && !strings.Contains(s, ":") && !strings.Contains(strings.ToLower(s), "bearer") &&
			!strings.Contains(s, "-") && !strings.Contains(s, "/") && !strings.Contains(s, "_") && !strings.Contains(s, "\x00") {
			// no separator, flag, URL or key shape: nothing to find (heuristics aside, which need 40 chars)
			if len(s) < 40 && out != s && plainASCIIWords(s) {
				t.Fatalf("changed text with nothing sensitive in it: %q -> %q", s, out)
			}
		}
	})
}

func plainASCIIWords(s string) bool {
	for _, r := range s {
		if !(r == ' ' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Patterns are linear (RE2), but the hand-written scanners are not regexes. Inputs built
// to stress them must finish.
func TestPathologicalInputsFinish(t *testing.T) {
	r := mustNew(t, redact.WithHeuristics())
	n := 1 << 18
	for name, in := range map[string]string{
		"password repeated":    strings.Repeat("password=x ", n/11),
		"one huge line":        strings.Repeat("a", n),
		"unterminated pem":     "-----BEGIN PRIVATE KEY-----" + strings.Repeat("A", n),
		"bearer repeated":      strings.Repeat("Bearer ", n/7),
		"token colon spaces":   "token=" + strings.Repeat(" ", n),
		"quotes":               strings.Repeat(`"`, n),
		"authorization nested": strings.Repeat("authorization: ", n/15),
		"cookie semicolons":    "Cookie: " + strings.Repeat("a=b;", n/4),
		"percent":              strings.Repeat("%3D", n/3),
		"userinfo":             strings.Repeat("a://b:c", n/7),
		"zero width":           strings.Repeat("\u200b", n/3),
		"fullwidth":            strings.Repeat("ａ", n/3),
		"base64ish 40":         strings.Repeat("aA1", n/3),
		"escaped quotes":       strings.Repeat(`\"`, n/2),
		"flags":                strings.Repeat("--token ", n/8),
		"config set":           strings.Repeat("config set ", n/11),
	} {
		out := r.Text(in)
		if again := r.Text(out); again != out {
			t.Errorf("%s: not idempotent", name)
		}
	}
}

func BenchmarkText(b *testing.B) {
	r, _ := redact.New()
	in := strings.Repeat("level=info msg=\"request done\" user=bob duration=12ms path=/api/v1/items?page=2\n", 5000)
	in += "Authorization: Bearer " + strings.Repeat("a1", 20) + "\n"
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		r.Text(in)
	}
}
