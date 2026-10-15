//go:build linux || darwin

package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type ownedContentRedactor struct {
	fail      bool
	expansion int
}

func (r ownedContentRedactor) RedactSnapshot(_ context.Context, b []byte) ([]byte, error) {
	if r.fail {
		return nil, errors.New("fake sensitive diagnostic must not escape")
	}
	if r.expansion > 0 {
		return bytes.Repeat([]byte("x"), r.expansion), nil
	}
	return bytes.ReplaceAll(b, []byte("fake-secret-value"), []byte("[REDACTED]")), nil
}
func TestCaptureRedactionKernelRunsBeforeAnyMirrorWrite(t *testing.T) {
	for _, kind := range []string{"redact", "refuse", "expand"} {
		t.Run(kind, func(t *testing.T) {
			config := ownedGuardedConfig(t)
			scope := config.Targets.roots[0]
			if e := os.WriteFile(filepath.Join(scope.Binding.Root, "source.txt"), []byte("prefix fake-secret-value suffix"), 0600); e != nil {
				t.Fatal(e)
			}
			mirror := t.TempDir()
			redactor := ownedContentRedactor{fail: kind == "refuse"}
			if kind == "expand" {
				redactor.expansion = 1000
			}
			_, e := prepareMirrorRedacted(context.Background(), scope, mirror, CaptureLimits{MaxBytes: 128, MaxFileBytes: 128, MaxEntries: 32}, nil, redactor)
			content, readErr := os.ReadFile(filepath.Join(mirror, "source.txt"))
			if kind == "redact" {
				if e != nil || readErr != nil || bytes.Contains(content, []byte("fake-secret-value")) || !bytes.Contains(content, []byte("[REDACTED]")) {
					t.Fatal("secret reached mirror or redacted output lost")
				}
			} else {
				if e == nil || !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("failed transform wrote raw mirror")
				}
				if bytes.Contains([]byte(e.Error()), []byte("sensitive")) {
					t.Fatal("raw transformer diagnostic escaped")
				}
			}
		})
	}
}
