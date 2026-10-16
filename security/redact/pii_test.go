package redact_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/security/redact"
)

func TestPIISelection(t *testing.T) {
	email := j("alice", "@", "example.test")
	ip := j("192.0.", "2.23")
	r := mustNew(t, redact.WithPII(redact.PIIClasses(redact.PIIEmail)))
	in := email + " " + ip + " phone=opaque email=opaque"
	want := redact.Marker + " " + ip + " phone=opaque email=" + redact.Marker
	if got := r.Text(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	for _, opts := range [][]redact.Option{
		{redact.WithPII(redact.PIIClasses())},
		{redact.WithPII(), redact.WithPII(redact.PIIClasses())},
	} {
		if got := mustNew(t, opts...).Text(in); got != in {
			t.Fatalf("empty selection: %q", got)
		}
	}
	if _, err := redact.New(redact.WithPII(redact.PIIClasses(redact.PIIClass(1 << 15)))); err == nil {
		t.Fatal("accepted unknown class")
	}
	keepIP := mustNew(t, redact.WithPII(redact.PIIClasses(redact.PIIAll&^redact.PIIIP)))
	if got := keepIP.Text("ip_address=" + ip); got != "ip_address="+ip {
		t.Fatalf("IP selection: %q", got)
	}
	r = mustNew(t, redact.WithPII(redact.PIIClasses(redact.PIIEmail)), redact.WithoutRules(redact.RuleEmail))
	if got := r.Text(email); got != email {
		t.Fatal("WithoutRules ignored")
	}
}

func TestPIINamesAndStructuredPaths(t *testing.T) {
	r := mustNew(t, redact.WithPII())
	for _, key := range []string{"email", "phone", "ssn", "address", "dob", "birth_date", "first_name", "last_name", "full_name", "customerEmail", "FIRST_NAME", "ｅｍａｉｌ", "e\u200dmail"} {
		t.Run(key, func(t *testing.T) {
			if !redact.PIIName(key) || redact.SensitiveName(key) {
				t.Fatal("name predicate")
			}
			data, err := r.Marshal(map[string]any{key: map[string]any{"opaque": "invented person"}})
			if err != nil || strings.Contains(string(data), "invented person") {
				t.Fatalf("JSON: %s %v", data, err)
			}
			args := r.Args([]string{"app", "--" + key, "invented person"})
			if args[2] != redact.Marker {
				t.Fatalf("Args: %q", args)
			}
			env := r.Env([]string{key + "=invented person"})
			if env[0] != key+"="+redact.Marker {
				t.Fatalf("Env: %q", env)
			}
			var log bytes.Buffer
			slog.New(r.Handler(slog.NewJSONHandler(&log, nil))).Info("event", slog.Group(key, slog.String("opaque", "invented person")))
			if strings.Contains(log.String(), "invented person") {
				t.Fatalf("slog group: %s", log.String())
			}
		})
	}
	for _, key := range []string{"email_count", "phone_type", "first_name_count", "full_name_format", "dob_required", "address_type", "email_name", "somebody", "hostname"} {
		if redact.PIIName(key) {
			t.Errorf("metadata: %s", key)
		}
	}
	// A key classifier protects values by name regardless of whether the value
	// resembles a valid date or identifier, and leaves bare dates alone.
	in := `{"birth_date":"invented","dob":42,"email_count":2,"phone_type":"mobile"}`
	out, err := r.JSON([]byte(in))
	if err != nil || string(out) != `{"birth_date":"[REDACTED]","dob":"[REDACTED]","email_count":2,"phone_type":"mobile"}` {
		t.Fatalf("JSON: %s %v", out, err)
	}
}

func TestPIIEntryPointsAndFindings(t *testing.T) {
	r := mustNew(t, redact.WithPII())
	value := j("δοκιμή", "@", "παράδειγμα.δοκιμή")
	if got := string(r.Bytes([]byte(value))); got != redact.Marker {
		t.Fatal(got)
	}
	if got := r.Tail("before " + value); got != "before "+redact.Marker {
		t.Fatal(got)
	}
	if got := r.ErrorText(errors.New(value)); got != redact.Marker {
		t.Fatal(got)
	}
	if got := r.ErrorText(redact.GuidanceWrap(errors.New(value), "retry safely")); strings.Contains(got, value) {
		t.Fatal(got)
	}
	var log bytes.Buffer
	slog.New(r.Handler(slog.NewTextHandler(&log, nil))).Info(value, "email", "opaque")
	if strings.Contains(log.String(), value) || strings.Contains(log.String(), "opaque") {
		t.Fatal(log.String())
	}
	for _, in := range []string{value, url.QueryEscape(value), j("ⓐlice", "＠", "example.test"), j("e\u0301lodie", "@", "exemple.test")} {
		text := "before <" + in + "> after"
		fs := r.Scan(text)
		if len(fs) != 1 || fs[0].Rule != redact.RuleEmail || text[fs[0].Start:fs[0].End] != in {
			t.Fatalf("offsets/class: %+v", fs)
		}
	}
	// Even when a credential rule owns the larger span, PII classification survives.
	in := `password="` + value + `"`
	fs := r.Scan(in)
	if len(fs) != 1 || fs[0].Rule != redact.RuleEmail {
		t.Fatalf("overlap: %+v", fs)
	}
}

func TestPIIMandatorySanitizer(t *testing.T) {
	opts := []redact.Option{
		redact.WithPII(redact.PIIClasses()),
		redact.WithoutRules(redact.RuleEmail, redact.RulePhone, redact.RuleIPv4, redact.RuleIPv6, redact.RuleCard, redact.RuleNaniteID, redact.RuleIBAN, redact.RuleSSN, redact.RuleNINO, redact.RulePIIName),
		redact.WithBenignKeys("email", "first_name"),
		redact.WithRule(redact.Rule{Name: redact.RuleEmail, Pattern: "NEVER_MATCH_THIS"}),
	}
	sanitizer, err := redact.NewSanitizer(redact.Policy{Mode: redact.Denylist}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range piiCorpus() {
		if c.Expect != "redact" {
			continue
		}
		payload, err := redact.Default().Marshal(map[string]string{"message": c.Input})
		if err != nil {
			t.Fatal(err)
		}
		out, rep := sanitizer.Sanitize(payload)
		if rep.ErrorClass != "" || !rep.PII || rep.Sensitivity != redact.Private {
			t.Fatalf("%s: %s %+v", c.ID, out, rep)
		}
		for _, secret := range c.Secret {
			if strings.Contains(string(out), secret) {
				t.Fatalf("%s survived", c.ID)
			}
		}
	}
	for _, policy := range []redact.Policy{{Mode: redact.Denylist}, {AllowedPaths: []string{"/first_name"}}, {}} {
		s, err := redact.NewSanitizer(policy)
		if err != nil {
			t.Fatal(err)
		}
		out, rep := s.Sanitize([]byte(j(`{"first_name":"Invented","password":"`, "invented-", `secret"}`)))
		if !rep.PII || rep.Sensitivity != redact.Secret || strings.Contains(string(out), "Invented") {
			t.Fatalf("%s %+v", out, rep)
		}
	}
}

func TestPIIWriterSplits(t *testing.T) {
	r := mustNew(t, redact.WithPII())
	for _, c := range piiCorpus() {
		in := "start\n" + c.Input + "\nend"
		want := r.Text(in)
		for split := 1; split < len(in); split++ {
			got := stream(t, r, in, func(i int) int {
				if i == 0 {
					return split
				}
				return len(in)
			})
			if got != want {
				t.Fatalf("%s split %d: %q != %q", c.ID, split, got, want)
			}
		}
		if got := stream(t, r, in, func(int) int { return 1 }); got != want {
			t.Fatalf("%s: %q != %q", c.ID, got, want)
		}
	}
	var out bytes.Buffer
	w := r.Writer(&out)
	for _, part := range []string{"address=\"invented first line\n", "invented second line\n"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "invented") {
			t.Fatal("unterminated multiline value emitted")
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if out.String() != `address="`+redact.Marker {
		t.Fatalf("close: %q", out.String())
	}
	// A bounded forced flush still retains complete values shorter than the hold.
	in := strings.Repeat("x", 300) + " " + j("alice", "@", "example.test") + " " + strings.Repeat("y", 300)
	if got := stream(t, r, in, func(int) int { return 1 }, redact.WithMaxPending(128), redact.WithHold(64)); got != r.Text(in) {
		t.Fatalf("forced: %q", got)
	}
}

// Exercise validators through the public detector, with unrelated classes off,
// so another detector cannot hide a missing validator.
func TestPIIValidators(t *testing.T) {
	tests := []struct {
		name  string
		class redact.PIIClass
		keep  []string
	}{
		{"iban", redact.PIIIBAN, []string{j("GB83", "WEST", "123456", "98765432"), j("GB82", "WEST", "123456", "9876543"), j("GB82", "WEST", "123456", "987654321"), j("XX82", "WEST", "123456", "98765432")}},
		{"ssn", redact.PIINationalID, []string{j("000-", "09-", "9999"), j("666-", "09-", "9999"), j("999-", "09-", "9999"), j("219-", "00-", "9999"), j("219-", "09-", "0000"), j("219", "09", "9999")}},
		{"email", redact.PIIEmail, []string{j("a..b", "@", "example.test"), j(".alice", "@", "example.test"), j("alice.", "@", "example.test"), j("alice", "@", "-example.test"), j("alice", "@", "example-.test"), j("alice", "@", "example..test"), j("alice", "@", "\u0301x.test"), j("alice", "@", strings.Repeat("a", 64), ".test"), j(strings.Repeat("a", 65), "@", "example.test")}},
		{"card", redact.PIICard, []string{j("4111", "1111", "1111", "1112")}},
		{"ip", redact.PIIIP, []string{j("999.0.", "2.23"), j("2001:db8:", ":xyz")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := mustNew(t, redact.WithPII(redact.PIIClasses(test.class)))
			for _, input := range test.keep {
				if got := r.Text(input); got != input {
					t.Errorf("false positive %q -> %q", input, got)
				}
			}
		})
	}
	r := mustNew(t, redact.WithPII(redact.PIIClasses(redact.PIINationalID)))
	for _, prefix := range []string{"BG", "GB", "KN", "NK", "NT", "TN", "ZZ", "DA", "FA", "IA", "QA", "UA", "VA", "AD", "AF", "AI", "AQ", "AU", "AV", "AO"} {
		input := j(prefix, "123456", "A")
		if got := r.Text(input); got != input {
			t.Errorf("invalid NINO prefix: %q", got)
		}
	}
	for _, suffix := range []string{"E", "F", "Z"} {
		input := j("AB", "123456", suffix)
		if got := r.Text(input); got != input {
			t.Errorf("invalid NINO suffix: %q", got)
		}
	}
}

func TestPIIWriterClosedMultiline(t *testing.T) {
	r := mustNew(t, redact.WithPII())
	for _, input := range []string{
		"before\naddress=\"invented first\ninvented second\" after\n",
		"before\n--address \"invented first\ninvented second\" after\n",
		"before\naddress=\\\"invented first\ninvented second\\\" after\n",
	} {
		want := r.Text(input)
		if strings.Contains(want, "invented") {
			t.Fatalf("Text leaked: %q", want)
		}
		if got := stream(t, r, input, func(int) int { return 1 }); got != want {
			t.Fatalf("%q != %q", got, want)
		}
	}
}

func FuzzPIIText(f *testing.F) {
	for _, c := range piiCorpus() {
		f.Add(c.Input)
	}
	r, err := redact.New(redact.WithPII())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 65536 {
			t.Skip()
		}
		out := r.Text(input)
		if again := r.Text(out); again != out {
			t.Fatalf("not idempotent: %q -> %q", out, again)
		}
	})
}
