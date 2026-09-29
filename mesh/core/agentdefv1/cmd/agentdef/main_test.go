package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exec(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("AGENTDEF_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch\n got: %q\nwant: %q", name, got, want)
	}
}

func TestValidate_Good(t *testing.T) {
	code, out, errs := exec(t, "validate", "testdata/good/incident-triage.md")
	if code != 0 || out != "" || errs != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestValidate_Bad(t *testing.T) {
	code, _, errs := exec(t, "validate", "testdata/bad/unknown.md", "testdata/bad/badname.md", "testdata/bad/dangling.md", "testdata/good/incident-triage.md")
	if code != 1 {
		t.Fatalf("code = %d", code)
	}
	golden(t, "validate_bad", errs)
}

func TestValidate_MissingFile(t *testing.T) {
	code, _, errs := exec(t, "validate", "testdata/nope.md")
	if code != 1 || !strings.HasPrefix(errs, "testdata/nope.md: ") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestLint(t *testing.T) {
	code, out, errs := exec(t, "lint", "testdata/lint/dup.md")
	if code != 0 || errs != "" {
		t.Fatalf("lint without --strict must exit 0 on findings: code=%d err=%q", code, errs)
	}
	golden(t, "lint_dup", out)

	if code, _, _ := exec(t, "lint", "--strict", "testdata/lint/dup.md"); code != 1 {
		t.Errorf("--strict code = %d, want 1", code)
	}
	if code, out, _ := exec(t, "lint", "--strict", "testdata/good/incident-triage.md"); code != 0 || out != "" {
		t.Errorf("clean fixture: code=%d out=%q", code, out)
	}
	if code, _, _ := exec(t, "lint", "testdata/bad/unknown.md"); code != 1 {
		t.Errorf("lint must fail on validate errors, code=%d", code)
	}
}

func TestLint_OrphanSkill(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.md"), "---\nname: a\ndescription: Does a useful thing for callers, when asked.\n---\nbody")
	write(t, filepath.Join(dir, "skills", "lonely", "SKILL.md"), "---\nname: lonely\ndescription: d\n---\n")
	code, out, _ := exec(t, "lint", filepath.Join(dir, "a.md"))
	if code != 0 || !strings.Contains(out, "orphan-skill") || !strings.Contains(out, "lonely") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestDigest(t *testing.T) {
	code, out, errs := exec(t, "digest", "testdata/good/incident-triage.md")
	if code != 0 || errs != "" {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	golden(t, "digest", out)

	code, out2, _ := exec(t, "digest", "--skills", "testdata/good/incident-triage.md")
	if code != 0 || !strings.HasPrefix(out2, out) || strings.Count(out2, "\n") != 2 {
		t.Fatalf("--skills should append one line per skill: %q", out2)
	}
	golden(t, "digest_skills", out2)

	if code, _, _ := exec(t, "digest", "testdata/bad/badname.md"); code != 1 {
		t.Errorf("digest of an invalid definition must fail, code=%d", code)
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "shared.md"), "rules v1")
	sum := sha256.Sum256([]byte("rules v1"))
	hash := "sha256:" + hex.EncodeToString(sum[:])
	body := "---\nname: gen\ndescription: Has generated text in its body.\n---\n<!-- agentdef:generated source=shared.md hash=" + hash + " -->\ntext\n<!-- /agentdef:generated -->\n"
	def := filepath.Join(dir, "gen.md")
	write(t, def, body)
	plain := filepath.Join(dir, "plain.md")
	write(t, plain, "---\nname: plain\ndescription: No generated spans at all.\n---\nbody")

	if code, _, errs := exec(t, "check", def, plain); code != 0 || errs != "" {
		t.Fatalf("fresh: code=%d err=%q", code, errs)
	}
	write(t, filepath.Join(dir, "shared.md"), "rules v2")
	code, _, errs := exec(t, "check", def, plain)
	if code != 1 || !strings.Contains(errs, "stale span source=shared.md") || strings.Contains(errs, "plain.md") {
		t.Fatalf("stale: code=%d err=%q", code, errs)
	}
}

func TestLayers(t *testing.T) {
	code, out, errs := exec(t, "digest", "--layer", "root=testdata/layers/team,precedence=1", "--layer", "root=testdata/layers/user,precedence=2")
	if code != 0 || errs != "" || strings.Count(out, "\n") != 1 || !strings.Contains(out, "user") {
		t.Fatalf("higher precedence must win: code=%d out=%q err=%q", code, out, errs)
	}
	code, _, errs = exec(t, "validate", "--layer", "root=testdata/layers/team", "--layer", "root=testdata/layers/user")
	if code != 1 || !strings.Contains(errs, "collision") || !strings.Contains(errs, "testdata/layers/team") || !strings.Contains(errs, "testdata/layers/user") {
		t.Fatalf("equal precedence must collide: code=%d err=%q", code, errs)
	}
	if code, _, _ := exec(t, "validate", "--layer", "precedence=1"); code != 2 {
		t.Errorf("layer without root: code=%d", code)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{}, {"bogus"}, {"validate"}, {"validate", "--force", "x.md"}, {"digest", "--layer", "root=x,precedence=z"},
	} {
		if code, _, _ := exec(t, args...); code != 2 {
			t.Errorf("%v: code = %d, want 2", args, code)
		}
	}
	if code, out, _ := exec(t, "help"); code != 0 || !strings.Contains(out, "usage:") {
		t.Errorf("help: code=%d", code)
	}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
