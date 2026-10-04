//go:build linux

package materialize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestInstalledFIFOHasNoOpenEvent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, ".claude/fixture.txt")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if _, err = syscall.InotifyAddWatch(fd, fifo, syscall.IN_OPEN); err != nil {
		t.Fatal(err)
	}
	if _, err = NewEngine(EngineOptions{}).Plan(context.Background(), installedFixtureRequest(t, root)); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatal("FIFO accepted")
	}
	buffer := make([]byte, 4096)
	n, err := syscall.Read(fd, buffer)
	if n > 0 || err != syscall.EAGAIN {
		t.Fatal("unsafe FIFO was opened before refusal")
	}
}
