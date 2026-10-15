//go:build unix

package procuse

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReviewCanonicalPathsBeforeControls(t *testing.T) {
	for _, kind := range []string{"target link", "scratch link into target", "scratch equal target", "scratch inside target"} {
		t.Run(kind, func(t *testing.T) {
			f, options := fixture(t)
			switch kind {
			case "target link":
				alias := filepath.Join(options.ScratchDir, "target-link")
				if err := os.Symlink(f.target, alias); err != nil {
					t.Fatal(err)
				}
				f.target = alias
			case "scratch link into target":
				alias := filepath.Join(options.ScratchDir, "scratch-link")
				if err := os.Symlink(f.target, alias); err != nil {
					t.Fatal(err)
				}
				options.ScratchDir = alias
			case "scratch equal target":
				options.ScratchDir = f.target
			case "scratch inside target":
				options.ScratchDir = filepath.Join(f.target, "scratch")
				if err := os.Mkdir(options.ScratchDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			got := Check(context.Background(), f.target, f.run, options)
			if got.Outcome != Unknown {
				t.Fatalf("unsafe physical roots accepted: %+v", got)
			}
			if strings.Contains(kind, "link") && got.Code != ReasonNonCanonicalPath {
				t.Fatalf("missing typed canonical refusal: %+v", got)
			}
			if len(f.calls) != 0 {
				t.Fatal("controls touched unsafe roots")
			}
		})
	}
}

func escapedName(path string) string {
	var b strings.Builder
	for _, c := range []byte(path) {
		if c >= 128 {
			fmt.Fprintf(&b, "\\x%02x", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func TestReviewEscapedNames(t *testing.T) {
	f, options := fixture(t)
	target := filepath.Join(options.ScratchDir, "café")
	if err := os.Rename(f.target, target); err != nil {
		t.Fatal(err)
	}
	f.target = target
	f.targetOpen.stdout = record(123, "7", escapedName(filepath.Join(target, "config")))
	if got := check(t, f, options); got.Outcome != InUse {
		t.Fatalf("escaped identity mismatch: %+v", got)
	}
	refs, ok := parseRecords(record(123, "7", `/fixture/tab\tline\nname`))
	if !ok || len(refs) != 1 || refs[0].name != "/fixture/tab\tline\nname" {
		t.Fatal("escaped path was not decoded")
	}
}

func TestReviewCwdControlDoesNotWalk(t *testing.T) {
	f, options := fixture(t)
	runner := func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		if len(args) > 0 && args[len(args)-1] == f.cwd && strings.Contains(strings.Join(args, " "), "+D") {
			return nil, nil, fmt.Errorf("cwd tree scan forbidden")
		}
		return f.run(ctx, args...)
	}
	if got := Check(context.Background(), f.target, runner, options); got.Outcome != NotInUse {
		t.Fatalf("caller tree was required: %+v", got)
	}
}

func TestReviewNilContextRetains(t *testing.T) {
	defer func() {
		if recover() != nil {
			t.Error("nil context panicked")
		}
	}()
	f, options := fixture(t)
	if got := Check(nil, f.target, f.run, options); got.Outcome != Unknown {
		t.Fatal(got)
	}
}

func TestReviewExecEnvironment(t *testing.T) {
	t.Setenv("LC_ALL", "invalid-locale")
	t.Setenv("PROCUSE_AMBIENT_MARKER", "must-not-inherit")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stdout, stderr, err := ExecRunner(os.Args[0])(ctx, "-test.run=^TestReviewExecHelper$", "--", "environment")
	if err != nil || len(stderr) != 0 || string(stdout) != "C|" {
		t.Fatalf("ambient environment inherited: %q, %q, %v", stdout, stderr, err)
	}
}

func TestReviewExecOutputLimits(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stdout, stderr, err := ExecRunner(os.Args[0])(ctx, "-test.run=^TestReviewExecHelper$", "--", stream)
		cancel()
		if err == nil || len(stdout) > 1<<20 || len(stderr) > 1<<20 {
			t.Fatalf("%s not bounded: out=%d err=%d failure=%v", stream, len(stdout), len(stderr), err)
		}
	}
}

func TestReviewExecTimeoutKillsGroup(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "pid")
	pulse := filepath.Join(root, "pulse")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, _ = ExecRunner(os.Args[0])(ctx, "-test.run=^TestReviewExecHelper$", "--", "parent", pidPath, pulse)
	bytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal("child did not start", err)
	}
	pid, err := strconv.Atoi(string(bytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	before, err := os.ReadFile(pulse)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(pulse)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("grandchild survived runner deadline")
	}
}

func TestReviewExecHelper(t *testing.T) {
	args := os.Args
	marker := -1
	for i, a := range args {
		if a == "--" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	args = args[marker+1:]
	switch args[0] {
	case "environment":
		fmt.Printf("%s|%s", os.Getenv("LC_ALL"), os.Getenv("PROCUSE_AMBIENT_MARKER"))
	case "stdout":
		fmt.Print(strings.Repeat("x", (1<<20)+1))
	case "stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", (1<<20)+1))
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestReviewExecHelper$", "--", "pulse", args[1], args[2])
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		_ = child.Wait()
	case "pulse":
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(3)
		}
		for n := 0; ; n++ {
			if err := os.WriteFile(args[2], []byte(strconv.Itoa(n)), 0600); err != nil {
				os.Exit(3)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	os.Exit(0)
}
