//go:build linux || darwin

package workspace_test

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMaterializeRejectsFIFOManifestAndReleasesLocks(t *testing.T) {
	p, f, s := applyFixture(t)
	if err := os.MkdirAll(filepath.Join(s.Home.Root.Path, ".materialize"), 0700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(s.Home.Root.Path, materialize.ManifestRelPath)
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for i := range f.observed.Roots {
		if f.observed.Roots[i].RootID == s.Home.Root.ID {
			f.observed.Roots[i].Exists = true
			f.observed.Roots[i].Directory = true
			f.observed.Roots[i].Empty = false
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := workspace.Materialize(ctx, p, f.ports()); done <- err }()
	select {
	case err := <-done:
		var refusal *workspace.Refusal
		if !errors.As(err, &refusal) || refusal.Code != workspace.CodeInvalidCommittedManifest {
			t.Fatalf("FIFO apply refusal: %v", err)
		}
		for _, event := range f.events {
			if strings.HasPrefix(event, "record:") || strings.HasPrefix(event, "directory:") {
				t.Fatal("unsafe manifest reached mutation boundary", f.events)
			}
		}
		if len(f.events) < 2 || f.events[len(f.events)-2] != "release:"+filepath.Base(p.LockKeys()[1].CanonicalID) || f.events[len(f.events)-1] != "release:"+filepath.Base(p.LockKeys()[0].CanonicalID) {
			t.Fatal("held locks not released in reverse", f.events)
		}
	case <-time.After(500 * time.Millisecond):
		fd, err := syscall.Open(fifo, syscall.O_RDWR|syscall.O_NONBLOCK, 0600)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("failed to unblock apply")
		}
		syscall.Close(fd)
		t.Fatal("apply retained full locks while cancelled manifest open waited for writer", f.events)
	}
}

func TestInspectRootRejectsFIFOManifestBeforeOpen(t *testing.T) {
	base := t.TempDir()
	ref := workspace.RootRef{ID: "root", Path: filepath.Join(base, "root"), AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
	if err := os.MkdirAll(filepath.Join(ref.Path, ".materialize"), 0700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(ref.Path, materialize.ManifestRelPath)
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := workspace.InspectRoot(ref); done <- err }()
	select {
	case err := <-done:
		var refusal *workspace.Refusal
		if !errors.As(err, &refusal) || refusal.Code != workspace.CodeInvalidCommittedManifest {
			t.Fatalf("FIFO manifest refusal: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		fd, err := syscall.Open(fifo, syscall.O_RDWR|syscall.O_NONBLOCK, 0600)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("failed to unblock FIFO reader")
		}
		syscall.Close(fd)
		t.Fatal("InspectRoot blocks opening nonregular manifest before checking its type")
	}
}
