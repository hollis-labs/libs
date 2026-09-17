package sftpsync

import (
	"path"
	"strings"
	"testing"
)

// TestEscapesRoot is the security check in isolation. It is lexical, so it can
// be exhaustively tabled without touching a filesystem — which is also why it
// gives the same answer on the local side and the remote one.
func TestEscapesRoot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rel    string
		target string
		want   bool
	}{
		{"sibling at the root", "current.yaml", "settings.yaml", false},
		{"sibling in a subdirectory", "config/current", "settings.yaml", false},
		{"down into a subdirectory", "top", "config/settings.yaml", false},
		{"up and back down, still inside", "config/sibling", "../config/settings.yaml", false},
		{"up and back down, deeper", "a/b/c/link", "../../d/file", false},
		{"dot", "link", ".", false},
		{"explicit current directory prefix", "link", "./settings.yaml", false},

		{"absolute unix target", "link", "/etc/shadow", true},
		{"absolute target that happens to be inside", "link", "/tmp/app/real.txt", true},
		{"bare parent", "link", "..", true},
		{"parent traversal", "link", "../secrets", true},
		{"traversal from a subdirectory", "config/link", "../../secrets", true},
		{"traversal disguised by a subdirectory", "link", "sub/../../etc/shadow", true},
		{"deep traversal that lands just outside", "a/b/link", "../../../x", true},
		{"empty target", "link", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapesRoot(tc.rel, tc.target); got != tc.want {
				t.Errorf("escapesRoot(%q, %q) = %v, want %v", tc.rel, tc.target, got, tc.want)
			}
		})
	}
}

// TestEscapesRootIsExactAtTheBoundary pins the off-by-one: a target that
// resolves to the root itself is inside it, one that resolves to a sibling of
// the root is not.
func TestEscapesRootIsExactAtTheBoundary(t *testing.T) {
	if escapesRoot("a/link", "..") {
		t.Error(`a/link -> ".." resolves to the root itself and is inside it`)
	}
	if !escapesRoot("a/link", "../..") {
		t.Error(`a/link -> "../.." resolves above the root and must be refused`)
	}
	// A name that merely starts with ".." is not traversal.
	if escapesRoot("link", "..hidden") {
		t.Error(`"..hidden" is an ordinary filename, not a traversal`)
	}
}

func TestTempRelIsASiblingAndIsHidden(t *testing.T) {
	for _, rel := range []string{"a.txt", "config/settings.yaml", "a/b/c/deep.bin"} {
		got, err := tempRel(rel)
		if err != nil {
			t.Fatalf("tempRel(%q): %v", rel, err)
		}
		if path.Dir(got) != path.Dir(rel) {
			// A temp file in another directory could be on another filesystem,
			// and the rename that follows would fail across it.
			t.Errorf("tempRel(%q) = %q: not a sibling", rel, got)
		}
		base := path.Base(got)
		if !strings.HasPrefix(base, ".") {
			t.Errorf("tempRel(%q) = %q: a half-written file should be hidden", rel, got)
		}
		if !strings.HasSuffix(base, tempSuffix) {
			t.Errorf("tempRel(%q) = %q: missing the %q marker", rel, got, tempSuffix)
		}
	}
}

func TestTempRelIsUniquePerCall(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		got, err := tempRel("config/settings.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if seen[got] {
			t.Fatalf("tempRel repeated %q; exclusive creation would fail", got)
		}
		seen[got] = true
	}
}

func TestTempRelBoundsTheNameLength(t *testing.T) {
	long := strings.Repeat("n", 400) + ".yaml"
	got, err := tempRel(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(path.Base(got)) > 255 {
		t.Errorf("temp name is %d bytes; most filesystems stop at 255", len(path.Base(got)))
	}
}

func TestNewConfigDefaults(t *testing.T) {
	cfg := newConfig(nil)
	if !cfg.preserveMode {
		t.Error("preserveMode should default on: a script that arrives non-executable fails an hour later")
	}
	if cfg.preserveModTime {
		t.Error("preserveModTime should default off")
	}
	if cfg.symlinks != SymlinkReplicate {
		t.Errorf("symlink policy defaults to %v, want SymlinkReplicate", cfg.symlinks)
	}
	if cfg.dryRun {
		t.Error("dryRun should default off")
	}
	if cfg.fileMode != defaultFileMode || cfg.dirMode != defaultDirMode {
		t.Errorf("modes default to %v/%v, want %v/%v", cfg.fileMode, cfg.dirMode, defaultFileMode, defaultDirMode)
	}
	if cfg.maxDepth != defaultMaxDepth {
		t.Errorf("maxDepth defaults to %d, want %d", cfg.maxDepth, defaultMaxDepth)
	}
}

func TestNewConfigRejectsNonsenseDepth(t *testing.T) {
	for _, depth := range []int{0, -1} {
		if got := newConfig([]Option{WithMaxDepth(depth)}).maxDepth; got != defaultMaxDepth {
			t.Errorf("WithMaxDepth(%d) gave %d, want the default %d", depth, got, defaultMaxDepth)
		}
	}
}

func TestOptionsAreIndependent(t *testing.T) {
	cfg := newConfig([]Option{
		nil, // a nil option must not panic; callers build these slices conditionally
		WithPreserveMode(false),
		WithFileMode(0o640),
		WithSymlinkPolicy(SymlinkSkip),
	})
	if cfg.preserveMode || cfg.fileMode != 0o640 || cfg.symlinks != SymlinkSkip {
		t.Errorf("options did not apply independently: %+v", cfg)
	}
	if cfg.dirMode != defaultDirMode {
		t.Errorf("an unset option changed: dirMode = %v", cfg.dirMode)
	}
}
