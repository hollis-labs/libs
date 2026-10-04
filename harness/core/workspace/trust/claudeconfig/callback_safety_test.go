package claudeconfig

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAlreadyPresentTargetChangeRefused(t *testing.T) {
	r := request(t)
	p := New()
	seed, e := p.Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = seed.Apply(context.Background(), r, func(context.Context) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if e = seed.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := p.Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	out, changed, e := s.Apply(context.Background(), r, func(context.Context) error {
		return os.Symlink(r.Config.Path, r.Target.LogicalPath)
	})
	if e == nil {
		t.Fatalf("accepted redirected target after authority callback: observation=%+v changed=%v", out, changed)
	}
}

func TestHardlinkReplacementKeepsOtherEntry(t *testing.T) {
	r := request(t)
	other := filepath.Join(r.Target.CanonicalParent, "unrelated.json")
	original := []byte(`{"unrelated":17}`)
	if e := os.WriteFile(other, original, 0600); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(r.Config.Path, configName)
	if e := os.Link(other, path); e != nil {
		t.Fatal(e)
	}
	s, e := New().Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	out, changed, e := s.Apply(context.Background(), r, func(context.Context) error { return nil })
	if e != nil || !changed || !out.Present {
		t.Fatal(out, changed, e)
	}
	actual, e := os.ReadFile(other)
	if e != nil || string(actual) != string(original) {
		t.Fatal("changed other hardlink", e)
	}
	a, e := os.Stat(other)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if os.SameFile(a, b) {
		t.Fatal("replacement retained old inode")
	}
}

func TestAlreadyPresentConfigurationChangeRefused(t *testing.T) {
	r := request(t)
	p := New()
	seed, e := p.Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = seed.Apply(context.Background(), r, func(context.Context) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if e = seed.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := p.Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	out, changed, e := s.Apply(context.Background(), r, func(context.Context) error {
		return os.WriteFile(filepath.Join(r.Config.Path, configName), []byte(`{"operator":true}`), 0600)
	})
	if e == nil {
		t.Fatalf("accepted removed trust after authority callback: observation=%+v changed=%v", out, changed)
	}
	actual, e := p.Observe(context.Background(), r)
	if e != nil || actual.Present {
		t.Fatal("witness did not remove trust", actual, e)
	}
}
