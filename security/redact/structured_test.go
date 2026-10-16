package redact_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/security/redact"
)

func TestJSONKeepsOrderNumbersAndValidity(t *testing.T) {
	r := mustNew(t)
	in := `{"z":1,"user":"bob","password":"hunter2synth","big":12345678901234567890,"n":1.50,"nested":{"api_key":"kk-synthetic-1","ok":true},` +
		`"list":["plain","token=Zz9-synthetic-1"],"max_tokens":1000,"usage":{"input_tokens":12},"missing_secrets":["OPENAI_API_KEY"],"credentials":{"user":"u","pass":"p"},"auth":null}`
	out, err := r.JSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"z":1,"user":"bob","password":"[REDACTED]","big":12345678901234567890,"n":1.50,"nested":{"api_key":"[REDACTED]","ok":true},` +
		`"list":["plain","token=[REDACTED]"],"max_tokens":1000,"usage":{"input_tokens":12},"missing_secrets":["OPENAI_API_KEY"],"credentials":"[REDACTED]","auth":null}`
	if string(out) != want {
		t.Errorf("\n got : %s\n want: %s", out, want)
	}
	if !json.Valid(out) {
		t.Error("output is not valid JSON")
	}
	again, _ := r.JSON(out)
	if !bytes.Equal(again, out) {
		t.Errorf("not idempotent:\n%s\n%s", out, again)
	}
}

func TestJSONErrorsAndHTMLCharacters(t *testing.T) {
	r := mustNew(t)
	for _, bad := range []string{`{"a":`, `not json`, `{"a":1} trailing`, ``} {
		if _, err := r.JSON([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	out, err := r.Marshal(map[string]any{"cmd": "a && b < c > d", "token": "tt-synthetic-1234"})
	if err != nil || !strings.Contains(string(out), "a && b < c > d") || strings.Contains(string(out), "tt-synthetic") {
		t.Errorf("%s %v", out, err)
	}
	pretty, _ := r.MarshalIndent(map[string]any{"password": "pw-synthetic-1", "a": 1}, "", "  ")
	if !strings.Contains(string(pretty), "\n  ") || strings.Contains(string(pretty), "pw-synthetic") {
		t.Errorf("%s", pretty)
	}
}

func TestArgs(t *testing.T) {
	r := mustNew(t)
	cases := []struct{ in, want []string }{
		{[]string{"run", "--token", "abc123synth", "--verbose"}, []string{"run", "--token", "[REDACTED]", "--verbose"}},
		{[]string{"run", "--password=hunter2synth"}, []string{"run", "--password=[REDACTED]"}},
		{[]string{"curl", "-u", "admin:s3cr3tSynth", "https://x.example.com"}, []string{"curl", "-u", "admin:[REDACTED]", "https://x.example.com"}},
		{[]string{"sshpass", "-p", "hunter2synth", "ssh", "host"}, []string{"sshpass", "-p", "[REDACTED]", "ssh", "host"}},
		{[]string{"mysql", "-u", "root", "-pSynthPass99", "db"}, []string{"mysql", "-u", "root", "-p[REDACTED]", "db"}},
		{[]string{"llm", "--max-tokens", "1000", "--password-file", "/run/secrets/x"}, []string{"llm", "--max-tokens", "1000", "--password-file", "/run/secrets/x"}},
		{[]string{"git", "clone", "https://bob:hunter2synth@example.com/r.git"}, []string{"git", "clone", "https://bob:[REDACTED]@example.com/r.git"}},
		{[]string{"docker", "run", "-u", "1000:1000", "alpine"}, []string{"docker", "run", "-u", "1000:1000", "alpine"}},
	}
	for _, c := range cases {
		in := append([]string(nil), c.in...)
		got := r.Args(c.in)
		if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
			t.Errorf("\n  in  : %q\n  got : %q\n  want: %q", c.in, got, c.want)
		}
		if strings.Join(c.in, "\x00") != strings.Join(in, "\x00") {
			t.Errorf("Args modified its input")
		}
	}
}

func TestEnv(t *testing.T) {
	got := mustNew(t).Env([]string{"PATH=/usr/bin", "DB_PASSWORD=hunter2synth", "API_URL=https://u:pw1234synth@h.example.com/x", "TOKEN_TYPE=bearer", "HOME=/home/u", "BARE"})
	want := []string{"PATH=/usr/bin", "DB_PASSWORD=[REDACTED]", "API_URL=https://u:[REDACTED]@h.example.com/x", "TOKEN_TYPE=bearer", "HOME=/home/u", "BARE"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("\n got : %q\n want: %q", got, want)
	}
}

func TestGuidanceKeepsProseAndRedactsTheCause(t *testing.T) {
	r := mustNew(t)
	cause := errors.New("upstream said: password=hunter2synth")
	err := redact.GuidanceWrap(cause, "credential_missing: reload the plugin %q", "onepassword")
	got := r.ErrorText(err)
	if got != `credential_missing: reload the plugin "onepassword": upstream said: password=[REDACTED]` {
		t.Errorf("%q", got)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is must reach the cause")
	}
	// the same text as a plain error is at the rules' mercy
	if plain := r.ErrorText(errors.New("credential_missing: reload the plugin")); plain != "credential_missing: reload the plugin" {
		t.Errorf("%q", plain)
	}
	if r.ErrorText(nil) != "" {
		t.Error("nil error")
	}
}

func TestErrorKeepsTheCauseReachable(t *testing.T) {
	r := mustNew(t)
	base := errors.New("connect failed token=Zz9-synthetic-1")
	wrapped := r.Error(base)
	if strings.Contains(wrapped.Error(), "Zz9") || !errors.Is(wrapped, base) {
		t.Errorf("%v", wrapped)
	}
	clean := errors.New("plain failure")
	if r.Error(clean) != clean {
		t.Error("an error with nothing to redact must be returned as it was")
	}
	if r.Error(nil) != nil {
		t.Error("nil")
	}
}

func TestSlogHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(mustNew(t).Handler(slog.NewJSONHandler(&buf, nil)))
	log.With("api_key", "kk-synthetic-1234").Info("login token=Zz9-synthetic-1",
		"user", "bob", "password", "hunter2synth", "count", 3, "max_tokens", 1000,
		slog.Group("req", slog.String("url", "https://u:pw1234synth@h.example.com"), slog.String("secret", "ss-synthetic-1")),
		"err", errors.New("failed: password=hunter2synth"))
	out := buf.String()
	for _, leak := range []string{"kk-synthetic", "Zz9-synthetic", "hunter2synth", "pw1234synth", "ss-synthetic"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked into the log: %s", leak, out)
		}
	}
	for _, keep := range []string{`"user":"bob"`, `"count":3`, `"max_tokens":1000`} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q was lost: %s", keep, out)
		}
	}
}
