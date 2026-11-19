package materialize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNoOpReconcileConfinesManifestAfterCallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "output")
	req := createRequest(root)
	first, err := NewEngine(EngineOptions{Now: fixedNow}).Apply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Operation = OperationReconcile
	req.CurrentManifest = &first.Manifest
	req.ExpectedGeneration = first.Manifest.Generation
	outside := t.TempDir()
	calls := 0
	engine := NewEngine(EngineOptions{Now: fixedNow, BeforeManifest: func(target string) error {
		calls++
		if target != root {
			t.Fatal("wrong callback target")
		}
		if err := os.Rename(filepath.Join(root, ".materialize"), filepath.Join(root, "saved-control")); err != nil {
			return err
		}
		return os.Symlink(outside, filepath.Join(root, ".materialize"))
	}})
	handle, err := engine.Apply(context.Background(), req)
	if err == nil || calls != 1 || handle.Report.Complete {
		t.Fatalf("unguarded no-op manifest commit: calls=%d handle=%+v err=%v", calls, handle, err)
	}
	files, err := os.ReadDir(outside)
	if err != nil || len(files) != 0 {
		t.Fatal("manifest escaped output root", files, err)
	}
}

func TestSaveManifestRejectsControlSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".materialize")); err != nil {
		t.Fatal(err)
	}
	if err := SaveManifest(root, Manifest{SchemaVersion: "materialize.v1"}); err == nil {
		t.Fatal("accepted control symlink")
	}
	files, err := os.ReadDir(outside)
	if err != nil || len(files) != 0 {
		t.Fatal("manifest escaped output root", files, err)
	}
}

func TestCreateStagesManifestBeforePublication(t *testing.T) {
	root := filepath.Join(t.TempDir(), "output")
	called := false
	engine := NewEngine(EngineOptions{Now: fixedNow, BeforeRename: func(stage, target string) error {
		called = true
		if _, err := LoadManifest(stage); err != nil {
			return err
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatal("target appeared before publish", err)
		}
		return nil
	}})
	if _, err := engine.Apply(context.Background(), createRequest(root)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("publication callback not called")
	}
}

func TestManifestCallbackFailureDoesNotPublishCreate(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "output")
	refused := errors.New("manifest admission refused")
	engine := NewEngine(EngineOptions{BeforeManifest: func(string) error { return refused }})
	if _, err := engine.Apply(context.Background(), createRequest(root)); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	files, err := os.ReadDir(parent)
	if err != nil || len(files) != 0 {
		t.Fatal("failed create left output or stage", files, err)
	}
}
