//go:build unix

package tokenfile

import (
	"syscall"
	"testing"
)

func mkfifo(t *testing.T, p string) {
	t.Helper()
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
}
