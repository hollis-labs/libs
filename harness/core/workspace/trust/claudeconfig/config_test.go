package claudeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/trust"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func request(t *testing.T) trust.Request {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "provider")
	parent := filepath.Join(base, "runtime")
	for _, p := range []string{home, parent} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	return trust.Request{Mechanism: trust.ClaudeProjects, Config: effects.RootInput{Path: home}, Target: trust.Target{LogicalPath: filepath.Join(parent, "current"), CanonicalParent: parent, AllowedBase: parent, Stable: true}}
}
func TestPreservesConfigurationAndIdempotence(t *testing.T) {
	r := request(t)
	p := New()
	ctx := context.Background()
	path := filepath.Join(r.Config.Path, ".claude.json")
	before := `{"privateFixture":"sentinel-secret-value","large":9007199254740993,"projects":{"unrelated":{"custom":7}}}`
	if e := os.WriteFile(path, []byte(before), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Observe(ctx, r); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(path + ".lock"); !os.IsNotExist(e) {
		t.Fatal("preflight created lock")
	}
	s, e := p.Begin(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	got, changed, e := s.Apply(ctx, r, func(context.Context) error { return nil })
	if e != nil || !changed || !got.Present {
		t.Fatal(got, changed, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "9007199254740993") || !strings.Contains(string(b), "sentinel-secret-value") {
		t.Fatal("lost unrelated data")
	}
	var obj map[string]json.RawMessage
	if e = json.Unmarshal(b, &obj); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(path)
	s, e = p.Begin(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	_, changed, e = s.Apply(ctx, r, func(context.Context) error { return nil })
	if e != nil || changed {
		t.Fatal(changed, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(st, after) {
		t.Fatal("rewrote already-present config")
	}
}
func TestMalformedShapesAndSymlinksRefused(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"projects":null}`, `{"projects":[]}`, `{"projects":{"TARGET":7}}`, `{"privateFixture":"sentinel-secret-value",`} {
		t.Run(body, func(t *testing.T) {
			r := request(t)
			body = strings.ReplaceAll(body, "TARGET", r.Target.LogicalPath)
			path := filepath.Join(r.Config.Path, ".claude.json")
			os.WriteFile(path, []byte(body), 0600)
			_, e := New().Observe(context.Background(), r)
			if e == nil || strings.Contains(e.Error(), "sentinel-secret-value") {
				t.Fatal(e)
			}
			after, _ := os.ReadFile(path)
			if string(after) != body {
				t.Fatal("changed refused config")
			}
		})
	}
	r := request(t)
	os.Symlink(filepath.Join(r.Config.Path, "elsewhere"), filepath.Join(r.Config.Path, ".claude.json"))
	if _, e := New().Observe(context.Background(), r); e == nil {
		t.Fatal("accepted symlink")
	}
}
func TestTargetResolutionNeverFallsBack(t *testing.T) {
	r := request(t)
	p := New()
	os.Symlink(r.Config.Path, r.Target.LogicalPath)
	if _, e := p.Observe(context.Background(), r); e == nil {
		t.Fatal("accepted redirected target")
	}
	os.Remove(r.Target.LogicalPath)
	r.Target.CanonicalParent = filepath.Join(r.Target.CanonicalParent, "absent")
	if _, e := p.Observe(context.Background(), r); e == nil {
		t.Fatal("invented canonical parent")
	}
}
func TestRetainsReplacedLock(t *testing.T) {
	r := request(t)
	s, e := New().Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(r.Config.Path, ".claude.json.lock")
	os.Remove(path)
	os.WriteFile(path, []byte("replacement"), 0600)
	if e = s.Close(); e == nil {
		t.Fatal("accepted replaced lock")
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "replacement" {
		t.Fatal("removed replacement")
	}
}

func TestLateConfigurationChangeRetained(t *testing.T) {
	r := request(t)
	path := filepath.Join(r.Config.Path, configName)
	os.WriteFile(path, []byte(`{"original":true}`), 0600)
	s, e := New().Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	calls := 0
	_, changed, e := s.Apply(context.Background(), r, func(context.Context) error {
		calls++
		if calls == 2 {
			return os.WriteFile(path, []byte(`{"operator":true}`), 0600)
		}
		return nil
	})
	if e == nil || changed {
		t.Fatal("overwrote changed configuration", changed, e)
	}
	b, _ := os.ReadFile(path)
	if string(b) != `{"operator":true}` {
		t.Fatal("lost operator change")
	}
}
func TestDuplicateKeysRefused(t *testing.T) {
	r := request(t)
	path := filepath.Join(r.Config.Path, configName)
	os.WriteFile(path, []byte(`{"unrelated":1,"unrelated":2}`), 0600)
	if _, e := New().Observe(context.Background(), r); e == nil {
		t.Fatal("accepted lossy duplicate keys")
	}
}

func TestPostRenameDurabilityFailureIsVisible(t *testing.T) {
	r := request(t)
	sessionPort, e := New().Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	s := sessionPort.(*session)
	defer s.Close()
	s.syncDirectory = func(*os.Root) error { return errors.New("sentinel-secret-value") }
	observed, changed, e := s.Apply(context.Background(), r, func(context.Context) error { return nil })
	if e == nil || !changed || !observed.Present || strings.Contains(e.Error(), "sentinel-secret-value") {
		t.Fatal(observed, changed, e)
	}
	actual, e := New().Observe(context.Background(), r)
	if e != nil || !actual.Present {
		t.Fatal("lost visible effect", actual, e)
	}
}
func TestUnsupportedPortBeforeMutation(t *testing.T) {
	r := request(t)
	p := &Port{platform: "unsupported"}
	if p.Supported(trust.ClaudeProjects) {
		t.Fatal("claimed support")
	}
	if _, e := p.Observe(context.Background(), r); e == nil {
		t.Fatal("observed")
	}
	if s, e := p.Begin(context.Background(), r); s != nil || e == nil {
		t.Fatal("acquired lock")
	}
	if _, e := os.Stat(filepath.Join(r.Config.Path, lockName)); !os.IsNotExist(e) {
		t.Fatal("changed unsupported root")
	}
}
func TestLockContentionAndRootSwapRetain(t *testing.T) {
	r := request(t)
	p := New()
	s, e := p.Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := p.Begin(context.Background(), r); other != nil || e == nil {
		t.Fatal("ignored provider lock")
	}
	moved := r.Config.Path + "-moved"
	os.Rename(r.Config.Path, moved)
	os.Mkdir(r.Config.Path, 0700)
	_, changed, e := s.Apply(context.Background(), r, func(context.Context) error { return nil })
	if e == nil || changed {
		t.Fatal("used moved root")
	}
	if e = s.Close(); e == nil {
		t.Fatal("cleaned changed namespace")
	}
	if _, e = os.Stat(filepath.Join(moved, lockName)); e != nil {
		t.Fatal("lost retained lock")
	}
}
