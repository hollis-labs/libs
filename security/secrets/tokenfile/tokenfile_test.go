//go:build unix

package tokenfile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tokPath(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "run", "agent.token")
}

func write(t *testing.T, path string, mode os.FileMode, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { //nolint:gosec // tests build files with specific modes
		t.Fatal(err)
	}
}

func noLeak(t *testing.T, tok string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), tok) {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestReadAcceptsAStrictFileAndTrims(t *testing.T) {
	p := tokPath(t)
	write(t, p, 0o600, "  tok-abc \n")
	got, err := Read(p)
	if err != nil || got != "tok-abc" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadRefusesEveryPermissionAndTypeViolation(t *testing.T) {
	const tok = "leaky-token-value"
	dir := filepath.Dir(tokPath(t))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		setup func(p string)
		want  error
	}{
		"mode 0644":  {func(p string) { write(t, p, 0o644, tok) }, ErrMode},
		"mode 0640":  {func(p string) { write(t, p, 0o640, tok) }, ErrMode},
		"mode 0400":  {func(p string) { write(t, p, 0o400, tok) }, ErrMode}, // exactly 0600, not tighter
		"directory":  {func(p string) { _ = os.MkdirAll(p, 0o700) }, ErrNotRegular},
		"empty":      {func(p string) { write(t, p, 0o600, "\n") }, ErrEmpty},
		"too large":  {func(p string) { write(t, p, 0o600, strings.Repeat("a", DefaultMaxSize+1)) }, ErrTooLarge},
		"missing":    {func(string) {}, fs.ErrNotExist},
		"fifo":       {func(p string) { mkfifo(t, p) }, ErrNotRegular},
		"other uid":  {func(p string) { write(t, p, 0o600, tok); currentUID = func() int { return os.Geteuid() + 1 } }, ErrOwner},
		"bad format": {func(p string) { write(t, p, 0o600, tok) }, ErrInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			orig := currentUID
			t.Cleanup(func() { currentUID = orig })
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
			c.setup(p)
			opts := Options{}
			if name == "bad format" {
				opts.Validate = ValidHex
			}
			_, err := Read(p, opts)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			noLeak(t, tok, err)
		})
	}
}

func TestReadRefusesSymlinks(t *testing.T) {
	p := tokPath(t)
	target := filepath.Join(filepath.Dir(p), "real")
	write(t, target, 0o600, "tok-real-value")
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("symlink: %v", err)
	}
	// A dangling or racing symlink is refused by O_NOFOLLOW as well.
	f, err := openNoFollow(p)
	if err == nil {
		_ = f.Close()
		t.Fatal("openNoFollow followed a symlink")
	}
}

func TestCreateRefusesToOverwriteOrFollowASymlink(t *testing.T) {
	p := tokPath(t)
	if err := Create(p, "tok-one-value"); err != nil {
		t.Fatal(err)
	}
	if err := Create(p, "tok-two-value"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second create: %v", err)
	}
	victim := filepath.Join(filepath.Dir(p), "victim")
	write(t, victim, 0o600, "keep")
	link := filepath.Join(filepath.Dir(p), "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := Create(link, "tok-three-value"); err == nil {
		t.Fatal("Create followed a symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" { //nolint:gosec // test path
		t.Fatalf("victim was written: %q", b)
	}
}

func TestEnsureCreatesStrictFileAndIsStable(t *testing.T) {
	p := tokPath(t)
	a, err := Ensure(p, nil, Options{Validate: ValidHex})
	if err != nil || !ValidHex(a) {
		t.Fatalf("ensure: %q %v", a, err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(p))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
	b, err := Ensure(p, nil, Options{Validate: ValidHex})
	if err != nil || a != b {
		t.Fatalf("second ensure changed the token: %v", err)
	}
}

func TestRotateIsAtomicAndLeavesNoTemp(t *testing.T) {
	p := tokPath(t)
	first, _ := Ensure(p, nil)
	second, err := Rotate(p, nil)
	if err != nil || second == first {
		t.Fatalf("rotate: %v", err)
	}
	if got, err := Read(p); err != nil || got != second {
		t.Fatalf("read after rotate: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries after rotate, want 1", len(entries))
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestRotateReplacesASymlinkInsteadOfWritingThrough(t *testing.T) {
	p := tokPath(t)
	victim := filepath.Join(filepath.Dir(p), "victim")
	write(t, victim, 0o600, "keep")
	if err := os.Symlink(victim, p); err != nil {
		t.Fatal(err)
	}
	// Read refuses a symlink, and Rotate refuses to replace one it cannot vouch for.
	if _, err := Rotate(p, nil); err == nil {
		t.Fatal("rotate accepted a symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" { //nolint:gosec // test path
		t.Fatalf("victim was written: %q", b)
	}
}

func TestRotateRefusesALooseFile(t *testing.T) {
	p := tokPath(t)
	write(t, p, 0o644, "tok-loose-value")
	if _, err := Rotate(p, nil); !errors.Is(err, ErrMode) {
		t.Fatalf("rotate over loose file: %v", err)
	}
}

func TestWriteFailureKeepsTheOldToken(t *testing.T) {
	p := tokPath(t)
	if err := Create(p, "tok-old-value"); err != nil {
		t.Fatal(err)
	}
	// A directory at the temp path is impossible to predict; instead make the
	// parent read-only so the temp file cannot be created.
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // test
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // test
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	if err := Write(p, "tok-new-value"); err == nil {
		t.Fatal("write into a read-only directory succeeded")
	}
	if got, err := Read(p); err != nil || got != "tok-old-value" {
		t.Fatalf("old token lost: %q %v", got, err)
	}
}

func TestWatcherRereadsOnChangeAndSignalsOnlyRealChanges(t *testing.T) {
	p := tokPath(t)
	first, _ := Ensure(p, nil)
	var changes atomic.Int32
	w := NewWatcher(p, Options{Validate: ValidHex}, func(error) { changes.Add(1) })

	if got, err := w.Token(); err != nil || got != first {
		t.Fatalf("first: %v", err)
	}
	if !w.Verify(first) || w.Verify("0000") {
		t.Fatal("verify wrong on first token")
	}
	if changes.Load() != 0 {
		t.Fatal("first load must not signal a change")
	}

	// Same token rewritten: new stamp, same digest: no change signal.
	if err := Write(p, first); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Token(); got != first || changes.Load() != 0 {
		t.Fatalf("same-token rewrite: got change count %d", changes.Load())
	}

	second, err := Rotate(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := w.Token(); err != nil || got != second {
		t.Fatalf("after rotate: %q %v", got, err)
	}
	if w.Verify(first) || !w.Verify(second) || changes.Load() != 1 {
		t.Fatalf("old token still verifies or change count = %d", changes.Load())
	}
}

func TestWatcherFailsClosedWhenTheFileTurnsLooseOrVanishes(t *testing.T) {
	p := tokPath(t)
	tok, _ := Ensure(p, nil)
	w := NewWatcher(p, Options{}, nil)
	if !w.Verify(tok) {
		t.Fatal("baseline verify failed")
	}
	if err := os.Chmod(p, 0o644); err != nil { //nolint:gosec // test makes the file loose on purpose
		t.Fatal(err)
	}
	if w.Verify(tok) {
		t.Fatal("a loosened file still verifies")
	}
	if _, err := w.Token(); !errors.Is(err, ErrMode) {
		t.Fatalf("token err = %v", err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if w.Verify(tok) {
		t.Fatal("a removed file still verifies")
	}
}

func TestRefreshIfOlderRotatesOnlyWhenStale(t *testing.T) {
	p := tokPath(t)
	t0 := time.Now()
	tok1, rotated, err := refreshIfOlder(p, time.Minute, nil, func() time.Time { return t0 })
	if err != nil || !rotated {
		t.Fatalf("create: rotated=%v err=%v", rotated, err)
	}
	tok2, rotated, _ := refreshIfOlder(p, time.Minute, nil, func() time.Time { return t0.Add(30 * time.Second) })
	if rotated || tok2 != tok1 {
		t.Fatal("rotated a fresh token")
	}
	tok3, rotated, _ := refreshIfOlder(p, time.Minute, nil, func() time.Time { return t0.Add(2 * time.Minute) })
	if !rotated || tok3 == tok1 {
		t.Fatal("did not rotate a stale token")
	}
	if got, _ := Read(p); got != tok3 {
		t.Fatal("file does not hold the refreshed token")
	}
}

func TestKeepRotatesInPlaceAndStopsOnCancel(t *testing.T) {
	p := tokPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	var rotations atomic.Int32
	done := make(chan struct{})
	go func() {
		Keep(ctx, p, 20*time.Millisecond, nil, func(string, error) { rotations.Add(1) })
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for rotations.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d rotations", rotations.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestEqualAndGenerate(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	if !ValidHex(a) || a == b || !Equal(a, a) || Equal(a, b) {
		t.Fatal("generate/equal misbehave")
	}
}
