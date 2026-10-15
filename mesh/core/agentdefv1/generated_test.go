package agentdef

import (
	"fmt"
	"testing"
	"testing/fstest"
)

func spanBody(src, hash string) string {
	return fmt.Sprintf("Intro.\n<!-- agentdef:generated source=%s hash=%s -->\nshared text\n<!-- /agentdef:generated -->\nOutro.", src, hash)
}

func TestCheckGenerated(t *testing.T) {
	fsys := fstest.MapFS{"shared/rules.md": {Data: []byte("rules v1")}}
	hash := sha256Hex([]byte("rules v1"))
	d := &Definition{Body: spanBody("shared/rules.md", hash)}

	stale, err := CheckGenerated(fsys, ".", d)
	if err != nil || len(stale) != 0 {
		t.Fatalf("unchanged source must pass: stale=%v err=%v", stale, err)
	}

	fsys["shared/rules.md"] = &fstest.MapFile{Data: []byte("rules v2")}
	stale, err = CheckGenerated(fsys, ".", d)
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale=%v err=%v", stale, err)
	}
	if stale[0].Span.Source != "shared/rules.md" || stale[0].Span.PinnedHash != hash || stale[0].ComputedHash != sha256Hex([]byte("rules v2")) {
		t.Errorf("stale span = %+v", stale[0])
	}
}

func TestCheckGenerated_OnlyTheStaleSpan(t *testing.T) {
	fsys := fstest.MapFS{"a.md": {Data: []byte("A")}, "b.md": {Data: []byte("B2")}}
	body := spanBody("a.md", sha256Hex([]byte("A"))) + "\n" + spanBody("b.md", sha256Hex([]byte("B")))
	stale, err := CheckGenerated(fsys, ".", &Definition{Body: body})
	if err != nil || len(stale) != 1 || stale[0].Span.Source != "b.md" {
		t.Fatalf("stale=%v err=%v", stale, err)
	}
}

func TestCheckGenerated_LayerRoot(t *testing.T) {
	fsys := fstest.MapFS{"root/s.md": {Data: []byte("x")}}
	stale, err := CheckGenerated(fsys, "root", &Definition{Body: spanBody("s.md", sha256Hex([]byte("x")))})
	if err != nil || len(stale) != 0 {
		t.Fatalf("stale=%v err=%v", stale, err)
	}
}

func TestCheckGenerated_ZeroSpans(t *testing.T) {
	stale, err := CheckGenerated(fstest.MapFS{}, ".", &Definition{Body: "plain body"})
	if err != nil || len(stale) != 0 {
		t.Fatalf("zero spans must pass trivially: %v %v", stale, err)
	}
}

func TestCheckGenerated_Errors(t *testing.T) {
	fsys := fstest.MapFS{"a.md": {Data: []byte("A")}}
	for name, body := range map[string]string{
		"missing source": spanBody("gone.md", "sha256:00"),
		"escaping":       spanBody("../a.md", "sha256:00"),
		"unclosed":       "<!-- agentdef:generated source=a.md hash=sha256:00 -->\ntext",
		"stray close":    "<!-- /agentdef:generated -->",
		"nested":         "<!-- agentdef:generated source=a.md hash=x -->\n<!-- agentdef:generated source=a.md hash=x -->\n<!-- /agentdef:generated -->",
		"no attrs":       "<!-- agentdef:generated -->\n<!-- /agentdef:generated -->",
		"unknown attr":   "<!-- agentdef:generated source=a.md hash=x color=red -->\n<!-- /agentdef:generated -->",
	} {
		if _, err := CheckGenerated(fsys, ".", &Definition{Body: body}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
