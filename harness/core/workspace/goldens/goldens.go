// Package goldens provides offline test support for the legacy planting corpus.
// It has no production callers. The workspace root remains reserved for the
// future merged package.
//
// Each case directory contains input.json, expected.json (a byte-exact tree
// including the root, directory modes and empty directories), and evidence.json
// (bindings, diagnostics and non-secret ownership/provenance). UTF-8 file bytes
// are readable JSON strings; arbitrary binary bytes use base64.
//
// Regenerate only through scripts/update-goldens at the repository root.
// It builds current consumers, renders into private scratch with fixture homes,
// scrubs execution roots, rejects residual private paths, scans every staged
// fixture, and shows the diff before --accept replaces expectations. The
// internal go-test stage flag writes only beneath TMPDIR, never to the corpus.
// Normal verification reads expectations and never invokes the updater.
// Only execution roots, session identifiers and timestamps are normalized;
// paths, permissions, argv order, diagnostics and ownership are preserved.
// Provider-specific posture and turn bindings live in providerplant cases;
// provider-neutral writers have no posture/bare launch API. agentsessions
// allocates a fresh disposable root and has no owned-refresh API. The context
// writer overwrites slot files without a manifest and retains unrelated files.
//
// Cairn seeds are archived evidence from its pinned source, not a live test
// dependency. They include boot and installed create/refresh, owned-key
// preservation, and the OpenCode installed-layer refusal. Re-capture explicitly:
//
//	scripts/update-goldens --cairn-repo <cairn-repo>
//
// Seed capture is also staged through scripts/update-goldens --cairn-repo.
// The script creates and removes a detached worktree, builds offline, and
// captures with an empty HOME and fixture provider homes before scanning.
package goldens

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.String("goldens", "", "internal golden render mode: stage (use scripts/update-goldens)")
var output = flag.String("goldens-output", "", "private staging directory beneath TMPDIR")

type Entry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Mode    string `json:"mode"`
	Content string `json:"content,omitempty"`
	Binary  string `json:"base64,omitempty"`
}
type Evidence struct {
	Writer      string   `json:"writer"`
	Source      string   `json:"source"`
	Bindings    any      `json:"bindings,omitempty"`
	Diagnostics []string `json:"diagnostics"`
	Ownership   any      `json:"ownership,omitempty"`
}
type Input struct {
	Provider string `json:"provider"`
	Runtime  string `json:"runtime,omitempty"`
	Variant  string `json:"variant,omitempty"`
	Posture  string `json:"posture,omitempty"`
	Scenario string `json:"scenario"`
}

// Load reads an authored case input; verification never derives expectations.
func Load(dir string) (Input, error) {
	var in Input
	b, err := os.ReadFile(filepath.Join(dir, "input.json"))
	if err != nil {
		return in, err
	}
	err = json.Unmarshal(b, &in)
	return in, err
}

// Cases discovers inputs in a family. A missing family is an error, not a skip.
func Cases(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "input.json" {
			out = append(out, filepath.Dir(p))
		}
		return nil
	})
	if err == nil && len(out) == 0 {
		err = fmt.Errorf("no golden inputs in %s", root)
	}
	return out, err
}

// Sandbox creates the render root with private fixture homes under TMPDIR.
// Tests using it must be serial because environment changes are process-wide.
func Sandbox(t *testing.T) string {
	t.Helper()
	if os.Getenv("TMPDIR") == "" {
		t.Fatal("golden tests require a private TMPDIR")
	}
	root := t.TempDir()
	for _, kv := range []struct{ k, p string }{{"HOME", "home"}, {"CODEX_HOME", "home/codex"}, {"OPENCODE_CONFIG_DIR", "home/opencode"}, {"XDG_CONFIG_HOME", "home/config"}} {
		p := filepath.Join(root, kv.p)
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(kv.k, p)
	}
	return root
}

// Roots replaces only explicitly supplied execution roots, in longest-first
// order provided by the caller. It never operates on a tree's path or mode.
func Roots(pairs ...string) func([]byte) []byte {
	r := strings.NewReplacer(pairs...)
	return func(b []byte) []byte { return []byte(r.Replace(string(b))) }
}

// Snapshot records every entry (including empty directories) with lstat modes.
// Symlinks are refused: these baseline writers manage regular files only.
func Snapshot(root string, normalize func([]byte) []byte) ([]Entry, error) {
	out := []Entry{}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		e := Entry{Path: filepath.ToSlash(rel), Mode: fmt.Sprintf("%04o", unixMode(info.Mode()))}
		if info.IsDir() {
			e.Kind = "directory"
		} else if info.Mode().IsRegular() {
			e.Kind = "file"
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if normalize != nil {
				b = normalize(b)
			}
			if rel == ".materialize/manifest.json" {
				// The timestamp is the only time-varying manifest field. Preserve all
				// other serialized bytes (including ownership, hashes and field order).
				rx := regexp.MustCompile(`("created_at": ")[^"]+`)
				b = rx.ReplaceAll(b, []byte(`${1}2000-01-01T00:00:00Z`))
			}
			if utf8.Valid(b) {
				e.Content = string(b)
			} else {
				e.Binary = base64.StdEncoding.EncodeToString(b)
			}
		} else {
			return fmt.Errorf("unsupported tree entry %s", rel)
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

// Check compares the tree and sidecar without rewriting either in normal mode.
// Update prints the actual unified diff BEFORE any expectation is replaced.
func Check(t *testing.T, dir, root string, ev Evidence, normalize func([]byte) []byte) {
	t.Helper()
	tree, err := Snapshot(root, normalize)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Diagnostics == nil {
		ev.Diagnostics = []string{}
	}
	compare(t, filepath.Join(dir, "expected.json"), tree, nil)
	compare(t, filepath.Join(dir, "evidence.json"), ev, normalize)
}
func compare(t *testing.T, path string, v any, normalize func([]byte) []byte) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	// JSON escaping means literal root replacement must precede marshaling for
	// unusual roots. Scratch roots used by this corpus have no escape characters.
	if normalize != nil {
		b = normalize(b)
	}
	want, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if *update != "" {
		if *update != "stage" {
			t.Fatal("regenerate through scripts/update-goldens")
		}
		temp := os.Getenv("TMPDIR")
		rel, e := filepath.Rel(temp, *output)
		if temp == "" || e != nil || !filepath.IsAbs(*output) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatal("golden output must be a private directory beneath TMPDIR")
		}
		abs, e := filepath.Abs(path)
		if e != nil {
			t.Fatal(e)
		}
		parts := strings.SplitN(filepath.ToSlash(abs), "/testdata/goldens/", 2)
		if len(parts) != 2 {
			t.Fatal("golden path is outside corpus")
		}
		destination := filepath.Join(*output, filepath.FromSlash(parts[1]))
		if e := os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(destination, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if bytes.Equal(want, b) {
		return
	}
	diffRoot := t.TempDir()
	old := filepath.Join(diffRoot, "before")
	next := filepath.Join(diffRoot, "after")
	if err := os.WriteFile(old, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, b, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("diff", "-u", "--label", filepath.Base(path)+" (expected)", "--label", filepath.Base(path)+" (actual)", old, next)
	diff, diffErr := cmd.CombinedOutput()
	if diffErr != nil {
		if e, ok := diffErr.(*exec.ExitError); !ok || e.ExitCode() != 1 {
			t.Fatal(diffErr)
		}
	}
	fmt.Printf("%s\n%s", filepath.ToSlash(path), diff)
	t.Errorf("golden differs: %s", filepath.ToSlash(path))
}

func unixMode(mode fs.FileMode) uint32 {
	bits := uint32(mode.Perm())
	if mode&fs.ModeSetuid != 0 {
		bits |= 04000
	}
	if mode&fs.ModeSetgid != 0 {
		bits |= 02000
	}
	if mode&fs.ModeSticky != 0 {
		bits |= 01000
	}
	return bits
}
