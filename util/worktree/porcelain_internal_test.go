package worktree

import (
	"reflect"
	"strings"
	"testing"
)

// TestParsePorcelain covers `git worktree list --porcelain` records: main
// tree, branch, detached, locked (with and without reason), prunable, bare,
// unknown attributes and CRLF.
func TestParsePorcelain(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []rawEntry
	}{
		{"empty", "", nil},
		{"main and branch", "worktree /r\nHEAD aaa\nbranch refs/heads/main\n\nworktree /w\nHEAD bbb\nbranch refs/heads/feat/x\n",
			[]rawEntry{{Path: "/r", HEAD: "aaa", Branch: "main"}, {Path: "/w", HEAD: "bbb", Branch: "feat/x"}}},
		{"detached", "worktree /w\nHEAD ccc\ndetached\n", []rawEntry{{Path: "/w", HEAD: "ccc", Detached: true}}},
		{"locked bare word", "worktree /w\nHEAD ccc\ndetached\nlocked\n", []rawEntry{{Path: "/w", HEAD: "ccc", Detached: true, Locked: true}}},
		{"locked with reason", "worktree /w\nHEAD ccc\ndetached\nlocked in use by a run\n", []rawEntry{{Path: "/w", HEAD: "ccc", Detached: true, Locked: true}}},
		{"prunable with reason", "worktree /w\nHEAD ccc\ndetached\nprunable gitdir file points to non-existent location\n", []rawEntry{{Path: "/w", HEAD: "ccc", Detached: true, Prunable: true}}},
		{"bare", "worktree /r.git\nbare\n", []rawEntry{{Path: "/r.git", Bare: true}}},
		{"path with spaces", "worktree /a b/c\nHEAD ddd\nbranch refs/heads/m\n", []rawEntry{{Path: "/a b/c", HEAD: "ddd", Branch: "m"}}},
		{"unknown attribute ignored", "worktree /w\nHEAD e\nfuture-thing 1\ndetached\n", []rawEntry{{Path: "/w", HEAD: "e", Detached: true}}},
		{"crlf and extra blanks", "worktree /w\r\nHEAD f\r\ndetached\r\n\r\n\r\nworktree /x\r\nHEAD g\r\n", []rawEntry{{Path: "/w", HEAD: "f", Detached: true}, {Path: "/x", HEAD: "g"}}},
		{"attribute before any worktree line", "HEAD zzz\nworktree /w\n", []rawEntry{{Path: "/w"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePorcelain(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePorcelain(%q)\n got %+v\nwant %+v", tt.in, got, tt.want)
			}
		})
	}
}

func FuzzParsePorcelain(f *testing.F) {
	f.Add("worktree /r\nHEAD a\nbranch refs/heads/main\n\nworktree /w\nHEAD b\ndetached\nlocked why\nprunable x\n")
	f.Add("")
	f.Add("worktree \n\n\nlocked\n")
	f.Fuzz(func(t *testing.T, in string) {
		got := parsePorcelain(in)
		for _, e := range got {
			if e.Path == "" {
				t.Fatalf("entry without path from %q", in)
			}
			if strings.ContainsAny(e.Path, "\n") {
				t.Fatalf("path contains newline: %q", e.Path)
			}
		}
		if n := strings.Count(in, "worktree "); len(got) > n {
			t.Fatalf("%d entries from %d worktree lines", len(got), n)
		}
	})
}

func TestGitEnvStripsAmbientRepoVars(t *testing.T) {
	env := gitEnv([]string{"PATH=/bin", "GIT_DIR=/x", "GIT_WORK_TREE=/y", "GIT_INDEX_FILE=/z", "LC_ALL=de_DE", "KEEP=1"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, bad := range []string{"\nGIT_DIR=", "\nGIT_WORK_TREE=", "\nGIT_INDEX_FILE=", "LC_ALL=de_DE"} {
		if strings.Contains(joined, bad) {
			t.Errorf("env still contains %q", bad)
		}
	}
	for _, want := range []string{"\nKEEP=1\n", "\nPATH=/bin\n", "\nGIT_TERMINAL_PROMPT=0\n", "\nLC_ALL=C\n", "\nGIT_OPTIONAL_LOCKS=0\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q", want)
		}
	}
}
