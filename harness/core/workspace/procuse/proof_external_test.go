//go:build unix

package procuse_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/procuse"
)

func TestHandBuiltResultIsNotProof(t *testing.T) {
	type cleanProof interface{ SafeToClean() bool }
	for _, r := range []procuse.Result{{}, {Outcome: procuse.NotInUse}, {Outcome: procuse.InUse}} {
		proof, ok := any(r).(cleanProof)
		if !ok {
			t.Fatal("no proof-checking API")
		}
		if proof.SafeToClean() {
			t.Fatal("caller forged a negative proof")
		}
	}
}

func TestOnlyCheckEarnsNegativeProof(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	live := false
	runner := func(_ context.Context, args ...string) ([]byte, []byte, error) {
		path := args[len(args)-1]
		fd := "3"
		if args[len(args)-2] == "-p" {
			path = cwd
			fd = "cwd"
		} else if strings.HasPrefix(filepath.Base(path), "procuse-control-") {
			path = filepath.Join(path, "held")
		} else {
			if !live {
				return nil, nil, nil
			}
			path = filepath.Join(target, "open")
			if len(args) > 1 && args[1] == "-d" {
				fd = "cwd"
				path = target
			}
		}
		return []byte(fmt.Sprintf("p%d\x00\nf%s\x00n%s\x00\n", os.Getpid(), fd, path)), nil, nil
	}
	r := procuse.Check(context.Background(), target, runner, procuse.Options{ScratchDir: root})
	if !r.SafeToClean() {
		t.Fatalf("earned proof missing: %+v", r)
	}
	r.Outcome = procuse.Unknown
	if r.SafeToClean() {
		t.Fatal("Unknown remained a negative proof")
	}
	live = true
	r = procuse.Check(context.Background(), target, runner, procuse.Options{ScratchDir: root})
	r.Outcome = procuse.NotInUse
	if r.SafeToClean() {
		t.Fatal("caller converted in-use observation into proof")
	}
}
