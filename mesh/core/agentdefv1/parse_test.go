package agentdef

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestParse_ValidFile(t *testing.T) {
	data := []byte(`---
name: test-agent
title: Test Agent
description: A test agent
identity: stable
tools:
  - read
  - write
skills:
  - go-build
requires: [mcp]
uses: [long-running]
hooks: [pre-tool-audit]
icon: code
avatar: https://example.com/a.png
tags:
  - backend
  - go
metadata:
  owner: platform
---
You are a test agent. Help the user with testing.
`)
	d, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Name != "test-agent" || d.Title != "Test Agent" || d.Description != "A test agent" {
		t.Errorf("identity fields = %q %q %q", d.Name, d.Title, d.Description)
	}
	if d.Identity != "stable" || d.Icon != "code" || d.Avatar != "https://example.com/a.png" {
		t.Errorf("scalars = %q %q %q", d.Identity, d.Icon, d.Avatar)
	}
	if len(d.Tools) != 2 || d.Tools[0] != "read" || d.Tools[1] != "write" {
		t.Errorf("Tools = %v", d.Tools)
	}
	if len(d.Skills) != 1 || d.Skills[0] != "go-build" || len(d.Tags) != 2 {
		t.Errorf("Skills/Tags = %v %v", d.Skills, d.Tags)
	}
	if d.Requires[0] != "mcp" || d.Uses[0] != "long-running" || d.Hooks[0] != "pre-tool-audit" {
		t.Errorf("requires/uses/hooks = %v %v %v", d.Requires, d.Uses, d.Hooks)
	}
	if d.Metadata["owner"] != "platform" {
		t.Errorf("Metadata = %v", d.Metadata)
	}
	if d.Body != "You are a test agent. Help the user with testing." {
		t.Errorf("Body = %q", d.Body)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestParse_UnknownField_IsError(t *testing.T) {
	// The exact frontmatter of Nanite's TestParseMD_AllFields fixture: Nanite
	// parses it tolerantly; v1 must reject it, naming the first unknown key.
	data := []byte(`---
name: Full Agent
slug: full-agent
description: All fields populated
icon: code
avatar: https://example.com/avatar.png
tags: [a, b]
model: gpt-4
tools: [read, write, bash]
permissionMode: yolo
maxTurns: 25
skills: [go-build, go-test]
mcpServers: [engine, tesseract]
memory: session
effort: high
isolation: worktree
directories: [./src, ./tests]
constraints:
  maxIterations: 50
  maxTimeSeconds: 300
  retryBudget: 3
modes:
  - slug: default
    name: Default
    promptAddendum: Be helpful.
  - slug: architect
    name: Architect
    promptAddendum: Focus on design.
    toolOverrides:
      prefer: [read, grep]
---
System prompt body here.
`)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for unknown fields")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Field != "slug" {
		t.Errorf("first error should name the first unknown key slug: %v", err)
	}
	if !strings.Contains(err.Error(), "slug") {
		t.Errorf("error text should name slug: %v", err)
	}
}

func TestParse_EmptyBody(t *testing.T) {
	d, err := Parse([]byte("---\nname: minimal\ndescription: d\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Body != "" {
		t.Errorf("Body = %q, want empty", d.Body)
	}
}

func TestParse_NoBody(t *testing.T) {
	d, err := Parse([]byte("---\nname: minimal\ndescription: d\n---"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Body != "" {
		t.Errorf("Body = %q, want empty", d.Body)
	}
}

func TestParse_MissingFrontmatter(t *testing.T) {
	if _, err := Parse([]byte("Just markdown.")); err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParse_MalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("---\n: invalid: yaml: [[\n---\nbody")); err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestParse_MissingName(t *testing.T) {
	_, err := Parse([]byte("---\ndescription: No name\n---\nbody"))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Field != "name" {
		t.Fatalf("want ValidationError on name, got %v", err)
	}
}

func TestParse_MissingDescription(t *testing.T) {
	_, err := Parse([]byte("---\nname: no-desc\n---\nbody"))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Field != "description" {
		t.Fatalf("want ValidationError on description, got %v", err)
	}
}

func TestParse_EmptyFrontmatter(t *testing.T) {
	if _, err := Parse([]byte("---\n---\nbody")); err == nil {
		t.Fatal("expected error: empty frontmatter has no name")
	}
}

func TestParse_MissingClosingDelimiter(t *testing.T) {
	if _, err := Parse([]byte("---\nname: broken\ndescription: d\n")); err == nil {
		t.Fatal("expected error for missing closing delimiter")
	}
}

func TestParse_ClosingDelimiterMustBeOwnLine(t *testing.T) {
	// "---x" inside the frontmatter is not a delimiter.
	_, err := Parse([]byte("---\nname: a\ndescription: d\n---x\n"))
	if err == nil {
		t.Fatal("expected error: ---x is not a closing delimiter")
	}
}

func TestParse_LeadingNewlines(t *testing.T) {
	d, err := Parse([]byte("\n\n---\nname: padded\ndescription: d\n---\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "padded" {
		t.Errorf("Name = %q", d.Name)
	}
}

func TestParse_CRLF(t *testing.T) {
	d, err := Parse([]byte("---\r\nname: crlf\r\ndescription: d\r\n---\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "crlf" || d.Body != "body" {
		t.Errorf("got %q %q", d.Name, d.Body)
	}
}

func TestParse_MultilineBody(t *testing.T) {
	d, err := Parse([]byte("---\nname: multi\ndescription: d\n---\nFirst paragraph.\n\nSecond paragraph with **markdown**.\n\n- List item 1\n- List item 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"First paragraph.", "Second paragraph", "- List item 2"} {
		if !strings.Contains(d.Body, want) {
			t.Errorf("Body missing %q: %q", want, d.Body)
		}
	}
}

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("---\nname: file-agent\ndescription: d\n---\nHello from file."), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := ParseFile(os.DirFS(dir), "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "file-agent" || d.SourceRef != "a.md" || d.Body != "Hello from file." {
		t.Errorf("got %+v", d)
	}
}

func TestParseFile_NotFound(t *testing.T) {
	if _, err := ParseFile(fstest.MapFS{}, "nope.md"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidate_NamePattern(t *testing.T) {
	for _, n := range []string{"a", "a-b", "a1-b2"} {
		if err := (&Definition{Name: n, Description: "d"}).Validate(); err != nil {
			t.Errorf("name %q rejected: %v", n, err)
		}
	}
	for _, n := range []string{"A", "a_b", "-a", "a-", "a--b", ""} {
		if err := (&Definition{Name: n, Description: "d"}).Validate(); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
}

func TestValidate_Identity(t *testing.T) {
	for _, id := range []string{"", "stable"} {
		if err := (&Definition{Name: "a", Description: "d", Identity: id}).Validate(); err != nil {
			t.Errorf("identity %q rejected: %v", id, err)
		}
	}
	for _, id := range []string{"true", "durable", "Stable", "anonymous"} {
		if err := (&Definition{Name: "a", Description: "d", Identity: id}).Validate(); err == nil {
			t.Errorf("identity %q accepted", id)
		}
	}
}

func TestValidate_Description(t *testing.T) {
	if err := (&Definition{Name: "a", Description: "  \n"}).Validate(); err == nil {
		t.Error("blank description accepted")
	}
}

func TestValidate_NamePatternsInLists(t *testing.T) {
	d := &Definition{Name: "a", Description: "d", Skills: []string{"Bad"}, Requires: []string{"x_y"}, Uses: []string{"-z"}, Hooks: []string{"H"}}
	err := d.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Errors) != 4 {
		t.Fatalf("want 4 field errors, got %v", err)
	}
	if !strings.Contains(err.Error(), "skills[0]") {
		t.Errorf("error should name the indexed field: %v", err)
	}
}

func TestValidate_ToolsAreNeverCatalogChecked(t *testing.T) {
	d := &Definition{Name: "a", Description: "d", Tools: []string{"Anything Goes", "x"}}
	if err := d.Validate(WithCapabilities(func(string) bool { return false })); err != nil {
		t.Errorf("tools must not be validated: %v", err)
	}
}

func TestValidate_WithCapabilities(t *testing.T) {
	d := &Definition{Name: "a", Description: "d", Requires: []string{"mcp"}, Uses: []string{"telepathy"}}

	if err := d.Validate(); err != nil {
		t.Errorf("no catalog: pattern-only expected, got %v", err)
	}
	known := func(n string) bool { return n == "mcp" }
	err := d.Validate(WithCapabilities(known))
	if err == nil || !strings.Contains(err.Error(), "uses[0]") || strings.Contains(err.Error(), "requires[0]") {
		t.Errorf("only uses[0] should be unknown: %v", err)
	}
	// hooks have no registry: never catalog-checked.
	d = &Definition{Name: "a", Description: "d", Hooks: []string{"whatever"}}
	if err := d.Validate(WithCapabilities(func(string) bool { return false })); err != nil {
		t.Errorf("hooks must not be catalog-checked: %v", err)
	}
}

func TestCanonicalDigest(t *testing.T) {
	base := func() *Definition {
		return &Definition{Name: "a", Description: "d", Tools: []string{"x", "y"}, Metadata: map[string]string{"a": "1", "b": "2", "c": "3"}, Body: "hi <b>&</b>"}
	}
	c1, err := Canonical(base())
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := Canonical(base())
	if string(c1) != string(c2) {
		t.Error("canonical form not deterministic")
	}
	if !strings.Contains(string(c1), "<b>&</b>") {
		t.Errorf("HTML must not be escaped: %s", c1)
	}
	d1, _ := Digest(base())
	d2, _ := Digest(base())
	if d1 != d2 || !strings.HasPrefix(d1, "sha256:") {
		t.Errorf("digests %q %q", d1, d2)
	}

	// Many metadata inserts in different orders: identical digest.
	for i := 0; i < 20; i++ {
		m := base()
		m.Metadata = map[string]string{}
		for _, k := range []string{"c", "a", "b"} {
			m.Metadata[k] = map[string]string{"a": "1", "b": "2", "c": "3"}[k]
		}
		if d, _ := Digest(m); d != d1 {
			t.Fatal("metadata map order changed the digest")
		}
	}

	// tools order is preserved, not sorted.
	swapped := base()
	swapped.Tools = []string{"y", "x"}
	if d, _ := Digest(swapped); d == d1 {
		t.Error("tools order must change the digest")
	}

	// Source location and body line endings do not.
	moved := base()
	moved.SourceRef, moved.Layer = "elsewhere.md", "L"
	moved.Body = "  hi <b>&</b>\r\n"
	if d, _ := Digest(moved); d != d1 {
		t.Error("SourceRef/Layer/body whitespace must not change the digest")
	}
	if _, err := Canonical(nil); err == nil {
		t.Error("Canonical(nil) should error")
	}
}

func TestLint(t *testing.T) {
	clean := &Definition{
		Name: "a", Description: "Investigates production incidents from an alert payload.",
		Tools: []string{"x", "y"}, Body: "Do the thing.",
	}
	if got := Lint(clean); len(got) != 0 {
		t.Errorf("clean definition produced findings: %+v", got)
	}

	bad := &Definition{
		Name: "a", Description: "Code reviewer", SourceRef: "a.md",
		Tools: []string{"x", "x"}, Requires: []string{"mcp"}, Uses: []string{"mcp"},
	}
	rules := map[string]bool{}
	for _, f := range Lint(bad) {
		rules[f.Rule] = true
		if f.Path != "a.md" || f.Severity != "warn" || f.Message == "" {
			t.Errorf("bad finding shape: %+v", f)
		}
	}
	for _, want := range []string{RuleDuplicateEntry, RuleRequiresUsesOverlap, RuleDescriptionLabel, RuleEmptyBody} {
		if !rules[want] {
			t.Errorf("rule %s did not fire", want)
		}
	}
}
