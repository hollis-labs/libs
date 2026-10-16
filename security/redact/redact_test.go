package redact_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hollis-labs/libs/security/redact"
)

func mustNew(t *testing.T, opts ...redact.Option) *redact.Redactor {
	t.Helper()
	r, err := redact.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSchemeAndSurroundingTextSurvive(t *testing.T) {
	r := mustNew(t)
	cases := map[string]string{
		"Authorization: Bearer " + j("abc", "DEF123456.tok-synthetic_value") + " done":                            "Authorization: Bearer [REDACTED] done",
		"Authorization: Basic dXNlcjpodW50ZXIyc3ludGg= done":                                                      "Authorization: Basic [REDACTED] done",
		`curl -H "Authorization: Bearer ` + j("abc", "DEF123456.tok-synthetic_value") + `" https://x.example.com`: `curl -H "Authorization: Bearer [REDACTED]" https://x.example.com`,
		`{"user":"bob","password":"hunter2-synthetic","ok":true}`:                                                 `{"user":"bob","password":"[REDACTED]","ok":true}`,
		"DATABASE_PASSWORD=Sup3rS3cretPw! next":                                                                   "DATABASE_PASSWORD=[REDACTED] next",
		"run --token abc123synthetic now":                                                                         "run --token [REDACTED] now",
		"GET https://bob:hunter2synth@example.com/path ok":                                                        "GET https://bob:[REDACTED]@example.com/path ok",
	}
	for in, want := range cases {
		if got := r.Text(in); got != want {
			t.Errorf("\n  in  : %q\n  got : %q\n  want: %q", in, got, want)
		}
	}
}

// Redacted JSON text must stay parseable: the markers go inside the quotes.
func TestQuotedValuesKeepTheirQuotes(t *testing.T) {
	out := mustNew(t).Text(`{"password":"x1","token":'y2',"a":"b"}`)
	if out != `{"password":"[REDACTED]","token":'[REDACTED]',"a":"b"}` {
		t.Errorf("%s", out)
	}
}

func TestCountsAndFindingsCarryNoSecrets(t *testing.T) {
	r := mustNew(t)
	in := "a password=Zz9-synthetic-value b token=Yy8-synthetic-value c"
	out, n := r.TextCount(in)
	if n != 2 || strings.Contains(out, "synthetic") {
		t.Fatalf("%d %q", n, out)
	}
	fs := r.Scan(in)
	if len(fs) != 2 || fs[0].Rule != redact.RuleAssignment {
		t.Fatalf("%+v", fs)
	}
	for _, f := range fs {
		if f.Start < 0 || f.End > len(in) || f.Start >= f.End {
			t.Errorf("bad range %+v", f)
		}
	}
	if _, n := r.TextCount(out); n != 0 {
		t.Errorf("redacting redacted text should count nothing, counted %d", n)
	}
}

func TestMarkerOptionAndEmptyMarkerRefused(t *testing.T) {
	r := mustNew(t, redact.WithMarker("<hidden>"))
	if got := r.Text("password=Zz9-synthetic-value"); got != "password=<hidden>" {
		t.Errorf("%q", got)
	}
	if got := r.Text(r.Text("password=Zz9-synthetic-value")); got != "password=<hidden>" {
		t.Errorf("custom marker not idempotent: %q", got)
	}
	if _, err := redact.New(redact.WithMarker("")); err == nil {
		t.Error("an empty marker would make redaction invisible and was accepted")
	}
}

// Fail closed: a rule the caller asked for must exist, or nothing runs.
func TestInvalidCustomRulesRefuseToStart(t *testing.T) {
	for name, rl := range map[string]redact.Rule{
		"bad pattern":   {Name: "x", Pattern: "("},
		"missing group": {Name: "y", Pattern: "a(b)", Group: 3},
		"negative":      {Name: "z", Pattern: "a", Group: -1},
	} {
		if r, err := redact.New(redact.WithRule(rl)); err == nil || r != nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r := mustNew(t, redact.WithRule(redact.Rule{Name: "ticket", Pattern: `INTERNAL-(\d{6})`, Group: 1}))
	if got := r.Text("see INTERNAL-123456 now"); got != "see INTERNAL-[REDACTED] now" {
		t.Errorf("%q", got)
	}
}

func TestWithoutRulesAndReferenceSchemes(t *testing.T) {
	in := "x-api-key: kk-synthetic-12345 ok"
	if got := mustNew(t, redact.WithoutRules(redact.RuleAssignment)).Text(in); got != in {
		t.Errorf("disabled rule still ran: %q", got)
	}
	r := mustNew(t, redact.WithReferenceSchemes("vault2"))
	if got := r.Text("DB_PASSWORD=vault2://team/db"); got != "DB_PASSWORD=vault2://team/db" {
		t.Errorf("a reference to a secret is not a secret: %q", got)
	}
	if got := r.Text("DB_PASSWORD=op://v/i/f"); got != "DB_PASSWORD=op://v/i/f" {
		t.Errorf("built-in reference scheme: %q", got)
	}
}

func TestSensitiveName(t *testing.T) {
	yes := []string{"password", "PASSWORD", "DB_PASSWORD", "db.password", "apiKey", "api_key", "API-KEY", "X-API-Key", "APIKey",
		"client_secret", "AWS_SECRET_ACCESS_KEY", "SECRET_KEY_BASE", "STRIPE_SECRET_KEY", "private_key", "x-auth-token",
		"access_token", "PGPASSWORD", "MYSQL_PWD", "credentials", "Cookie", "authorization", "GITHUB_TOKEN", "bearer"}
	no := []string{"tokenizer", "author", "keyboard", "passport", "key", "monkey", "max_tokens", "input_tokens", "total_tokens",
		"PASSWORD_MIN_LENGTH", "SECRET_ROTATION_DAYS", "token_type", "secret_name", "credential_missing", "missing_secrets",
		"token_endpoint", "PWD", "PATH", "HOME", "user", "name", "aws_access_key_id", "password_policy", "api_key_ref", "cookies"}
	for _, n := range yes {
		if !redact.SensitiveName(n) {
			t.Errorf("%q should be sensitive", n)
		}
	}
	for _, n := range no {
		if redact.SensitiveName(n) {
			t.Errorf("%q should not be sensitive", n)
		}
	}
}

func TestCustomKeyPolicy(t *testing.T) {
	r := mustNew(t, redact.WithSensitiveKeys("signing_blob"), redact.WithBenignKeys("token_bucket"))
	if got := r.Text("signing_blob=abcdef123456 token_bucket=abcdef123456"); got != "signing_blob=[REDACTED] token_bucket=abcdef123456" {
		t.Errorf("%q", got)
	}
}

// Looking through zero-width and fullwidth characters must not damage what is around them.
func TestNormalizationIsLookedThroughButTheOriginalIsWhatChanges(t *testing.T) {
	r := mustNew(t)
	zw := "​"
	cases := []struct{ in, want string }{
		{"key is " + j("sk-", "Ab3dEf6hIj9k") + zw + "Lm2nOp5qRs8tUv1wXy4Z end", "key is [REDACTED] end"},
		{"ｐａｓｓｗｏｒｄ=hunter2synth end", "ｐａｓｓｗｏｒｄ=[REDACTED] end"},
		{"pass" + zw + "word=hunter2synth end", "pass" + zw + "word=[REDACTED] end"},
		{"é ü 日本 password=Zz9-synth end ñ", "é ü 日本 password=[REDACTED] end ñ"},
	}
	for _, c := range cases {
		got := r.Text(c.in)
		got = strings.ReplaceAll(got, zw, "")
		want := strings.ReplaceAll(c.want, zw, "")
		if got != want {
			t.Errorf("\n  in  : %q\n  got : %q\n  want: %q", c.in, got, want)
		}
	}
}

func TestEncodedForms(t *testing.T) {
	r := mustNew(t)
	for _, in := range []string{
		"body=user%3Dbob%26password%3Dhunter2synth%26x%3D1",
		`{"log":"payload={\"password\":\"hunter2synth\"}"}`,
	} {
		if out := r.Text(in); strings.Contains(out, "hunter2synth") {
			t.Errorf("survived: %q -> %q", in, out)
		}
	}
}

func TestNewlinesInvalidUTF8AndLargeLinesArePreserved(t *testing.T) {
	r := mustNew(t)
	in := "line1\r\npassword=Zz9-synth\n\n\tindented\n\xff\xfe tail"
	out := r.Text(in)
	if strings.Count(out, "\n") != strings.Count(in, "\n") || !strings.Contains(out, "\r\n") || !strings.Contains(out, "\xff\xfe tail") {
		t.Errorf("layout changed: %q", out)
	}
	if utf8.ValidString(in) != utf8.ValidString(out) {
		t.Error("validity of UTF-8 changed")
	}
	if got := r.Text(""); got != "" {
		t.Error(got)
	}
}

func TestTruncatedPEMIsRedactedToTheEndOfTheText(t *testing.T) {
	out := mustNew(t).Text("log start\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\nBBBBCCCCDDDD\n")
	if strings.Contains(out, "MIIEvQ") || strings.Contains(out, "BBBBCCCC") || !strings.HasPrefix(out, "log start\n") {
		t.Errorf("%q", out)
	}
}

func TestDefaultAndPackageText(t *testing.T) {
	if redact.Default() != redact.Default() {
		t.Error("Default must be shared")
	}
	if got := redact.Text("password=Zz9-synth"); got != "password=[REDACTED]" {
		t.Error(got)
	}
}

func TestBytes(t *testing.T) {
	if got := string(redact.Default().Bytes([]byte("token=Zz9-synthetic-1"))); got != "token=[REDACTED]" {
		t.Error(got)
	}
}

func TestAuthorizationEdgeCases(t *testing.T) {
	r := mustNew(t)
	cred := j("abcDEF", "123456.tok-synthetic_value")
	cases := map[string]string{
		"Authorization: Bearer":                                                                       "Authorization: Bearer",
		"Authorization: Bearer <token>":                                                               "Authorization: Bearer <token>",
		"Authorization: Bearer " + cred + ".":                                                         "Authorization: Bearer [REDACTED].",
		"Authorization: " + cred + " and more":                                                        "Authorization: [REDACTED] and more",
		"Authorization: Token " + cred + ", x=1":                                                      "Authorization: Token [REDACTED], x=1",
		`Authorization: Digest username="u", realm="r", response="` + cred + `"`:                      `Authorization: Digest [REDACTED]`,
		`{"Authorization":"Bearer ` + cred + `"}`:                                                     `{"Authorization":"Bearer [REDACTED]"}`,
		"a Authorization: Basic " + j("dXNlcjpw", "YXNz") + " b Authorization: Bearer " + cred + " c": "a Authorization: Basic [REDACTED] b Authorization: Bearer [REDACTED] c",
		"Proxy-Authorization: Basic " + j("dXNlcjpw", "YXNz"):                                         "Proxy-Authorization: Basic [REDACTED]",
		"authorization = Bearer " + cred:                                                              "authorization = Bearer [REDACTED]",
	}
	for in, want := range cases {
		got := r.Text(in)
		if got != want {
			t.Errorf("\n  in  : %q\n  got : %q\n  want: %q", in, got, want)
		}
		if again := r.Text(got); again != got {
			t.Errorf("not idempotent: %q -> %q", got, again)
		}
	}
}
