package claudeconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func temporaryPath(t *testing.T, root string) string {
	t.Helper()
	entries, e := os.ReadDir(root)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".claude-trust-") && strings.HasSuffix(entry.Name(), ".tmp") {
			return filepath.Join(root, entry.Name())
		}
	}
	t.Fatal("no staged file")
	return ""
}
func TestRetainsReplacedTemporaryEntry(t *testing.T) {
	r := request(t)
	path := filepath.Join(r.Config.Path, configName)
	if e := os.WriteFile(path, []byte(`{"original":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New().Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	calls := 0
	staged := ""
	_, changed, e := s.Apply(context.Background(), r, func(context.Context) error {
		calls++
		if calls != 2 {
			return nil
		}
		staged = temporaryPath(t, r.Config.Path)
		if e := os.Rename(staged, staged+".saved"); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(staged, []byte("foreign"), 0600); e != nil {
			t.Fatal(e)
		}
		return errors.New("fixture authority revoked")
	})
	if e == nil || !changed {
		t.Fatal("uncertain staging was not retained", changed, e)
	}
	foreign, e := os.ReadFile(staged)
	if e != nil || string(foreign) != "foreign" {
		t.Fatal("foreign temporary entry removed", e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != `{"original":true}` {
		t.Fatal("original configuration changed", e)
	}
}
func TestTemporaryIdentityCheckedBeforeReplacement(t *testing.T) {
	r := request(t)
	path := filepath.Join(r.Config.Path, configName)
	if e := os.WriteFile(path, []byte(`{"original":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New().Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	calls := 0
	staged := ""
	_, changed, e := s.Apply(context.Background(), r, func(context.Context) error {
		calls++
		if calls != 2 {
			return nil
		}
		staged = temporaryPath(t, r.Config.Path)
		if e := os.Rename(staged, staged+".saved"); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(staged, []byte(`{"foreign":true}`), 0600); e != nil {
			t.Fatal(e)
		}
		return nil
	})
	if e == nil || !changed {
		t.Fatal("replaced staging was accepted", changed, e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != `{"original":true}` {
		t.Fatal("foreign staging replaced configuration", e)
	}
	if _, e := os.Stat(staged); e != nil {
		t.Fatal("foreign entry removed", e)
	}
}
func TestRootCustodyLossRetainsStaging(t *testing.T) {
	r := request(t)
	if e := os.WriteFile(filepath.Join(r.Config.Path, configName), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New().Begin(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	calls := 0
	old := r.Config.Path + "-previous"
	staged := ""
	_, changed, e := s.Apply(context.Background(), r, func(context.Context) error {
		calls++
		if calls != 2 {
			return nil
		}
		staged = filepath.Base(temporaryPath(t, r.Config.Path))
		if e := os.Rename(r.Config.Path, old); e != nil {
			t.Fatal(e)
		}
		if e := os.Mkdir(r.Config.Path, 0700); e != nil {
			t.Fatal(e)
		}
		return nil
	})
	if e == nil || !changed {
		t.Fatal("lost custody not reported", changed, e)
	}
	if _, e := os.Stat(filepath.Join(old, staged)); e != nil {
		t.Fatal("mutated staging outside declared root", e)
	}
}
