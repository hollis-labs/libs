package ghmerged

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	worktree "github.com/hollis-labs/libs/util/worktree"
)

// fakeGH writes a shell script standing in for gh. It records its arguments
// and working directory next to itself, then runs body.
func fakeGH(t *testing.T, body string) (bin, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script gh stand-in")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "gh")
	record = filepath.Join(dir, "record")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > " + record + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // test stand-in must be executable
		t.Fatal(err)
	}
	return bin, record
}

func TestMerged_ParsesRunsInRepoAndPassesArgs(t *testing.T) {
	bin, record := fakeGH(t, `cat <<'JSON'
[{"headRefName":"fix/CW-1","headRefOid":"aaa"},{"headRefName":"","headRefOid":"bbb"},{"headRefName":"feat/x","headRefOid":"ccc"}]
JSON`)
	repo := t.TempDir()
	got, err := New(WithBinary(bin), WithLimit(7)).Merged(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: "aaa"}, {HeadRef: "feat/x", HeadOID: "ccc"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	rec, err := os.ReadFile(record) //nolint:gosec // path is a test temp file
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(rec)), "\n")
	if wd, _ := filepath.EvalSymlinks(repo); lines[0] != wd && lines[0] != repo {
		t.Errorf("gh ran in %q, want %q", lines[0], repo)
	}
	wantArgs := []string{"pr", "list", "--state", "merged", "--json", "headRefName,headRefOid", "--limit", "7"}
	if !reflect.DeepEqual(lines[1:], wantArgs) {
		t.Errorf("args = %v, want %v", lines[1:], wantArgs)
	}
}

func TestMerged_DefaultLimit(t *testing.T) {
	bin, record := fakeGH(t, "echo '[]'")
	if _, err := New(WithBinary(bin), WithLimit(0)).Merged(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	rec, _ := os.ReadFile(record) //nolint:gosec // path is a test temp file
	if !strings.HasSuffix(strings.TrimSpace(string(rec)), "--limit\n200") {
		t.Errorf("record = %q", rec)
	}
}

func TestMerged_MissingBinaryIsErrUnavailable(t *testing.T) {
	_, err := New(WithBinary(filepath.Join(t.TempDir(), "no-such-gh"))).Merged(context.Background(), t.TempDir())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestMerged_FailureCarriesStderr(t *testing.T) {
	bin, _ := fakeGH(t, "echo 'gh: not logged in' >&2\nexit 4")
	_, err := New(WithBinary(bin)).Merged(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("a failing gh is not ErrUnavailable")
	}
}

func TestMerged_HonoursContext(t *testing.T) {
	bin, _ := fakeGH(t, "sleep 5")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(WithBinary(bin)).Merged(ctx, t.TempDir()); err == nil {
		t.Fatal("expected an error from a canceled context")
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name, in string
		want     []worktree.MergedRef
		wantErr  bool
	}{
		{"empty list", `[]`, []worktree.MergedRef{}, false},
		{"missing oid keeps ref", `[{"headRefName":"a"}]`, []worktree.MergedRef{{HeadRef: "a"}}, false},
		{"not json", `nope`, nil, true},
		{"object not array", `{}`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parse([]byte(tt.in))
			if (err != nil) != tt.wantErr || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parse(%q) = %+v, %v", tt.in, got, err)
			}
		})
	}
}
