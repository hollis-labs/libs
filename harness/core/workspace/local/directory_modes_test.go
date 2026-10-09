//go:build linux || darwin

package local_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/local"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

func directoryTree(mode os.FileMode, body string) artifact.Tree {
	return artifact.Tree{Entries: []artifact.Entry{
		{Path: "hooks", Kind: artifact.EntryDirectory, Mode: mode, Ownership: artifact.Ownership{EntryID: "hooks", GroupID: "fixture"}, Provenance: artifact.Provenance{Source: "fixture"}},
		{Path: "hooks/run", Kind: artifact.EntryFile, Mode: 0700, Bytes: []byte(body), Ownership: artifact.Ownership{EntryID: "run", GroupID: "fixture"}, Provenance: artifact.Provenance{Source: "fixture"}},
		{Path: "hooks/nested", Kind: artifact.EntryDirectory, Mode: 0750, Ownership: artifact.Ownership{EntryID: "nested", GroupID: "fixture"}, Provenance: artifact.Provenance{Source: "fixture"}},
		{Path: "hooks/nested/context.md", Kind: artifact.EntryFile, Mode: 0600, Bytes: []byte(body), Ownership: artifact.Ownership{EntryID: "context", GroupID: "fixture"}, Provenance: artifact.Provenance{Source: "fixture"}},
	}}
}

func TestAuthoredDirectoryModesSurviveCreateRefreshAndReconcile(t *testing.T) {
	for _, mode := range []os.FileMode{0, 0700, 0750, 0755} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			options, request := fixture(t)
			var generation string
			for i, operation := range []materialize.Operation{materialize.OperationCreate, materialize.OperationRefresh, materialize.OperationReconcile} {
				options.OperationID = fmt.Sprintf("directory-operation-%d", i)
				request.OperationID = options.OperationID
				request.Operation = operation
				request.Generation = fmt.Sprintf("directory-generation-%d", i)
				request.ExpectedGeneration = generation
				wanted := mode
				if i == 2 {
					// Reconcile an owned directory to a different private mode.
					wanted = 0750
					if mode == 0750 {
						wanted = 0700
					}
				}
				body := fmt.Sprintf("hook revision %d", i)
				request.Tree = directoryTree(wanted, body)
				observed, err := workspace.InspectRoot(request.Root)
				if err != nil {
					t.Fatal(err)
				}
				request.Observed.Roots[0] = observed
				options.Evidence = func(context.Context) (workspace.Observations, error) { return request.Observed, nil }
				ports, closePorts, err := local.New(options)
				if err != nil {
					t.Fatal(err)
				}
				result, applyErr := workspace.ApplyTree(context.Background(), request, ports)
				closeErr := closePorts()
				if applyErr != nil || closeErr != nil || !result.ArtifactsComplete() || len(result.Handles) != 1 {
					t.Fatalf("%s: result=%+v apply=%v close=%v", operation, result, applyErr, closeErr)
				}
				if wanted == 0 {
					wanted = 0755
				}
				info, err := os.Stat(filepath.Join(request.Root.Path, "hooks"))
				if err != nil || !info.IsDir() || info.Mode().Perm() != wanted {
					t.Fatalf("%s: directory mode not preserved: info=%v err=%v want=%04o", operation, info, err, wanted)
				}
				data, err := os.ReadFile(filepath.Join(request.Root.Path, "hooks/run"))
				if err != nil || string(data) != body {
					t.Fatalf("%s: hook content=%q err=%v", operation, data, err)
				}
				hook, err := os.Stat(filepath.Join(request.Root.Path, "hooks/run"))
				if err != nil || hook.Mode().Perm() != 0700 {
					t.Fatalf("%s: executable file mode changed: %v %v", operation, hook, err)
				}
				nested, err := os.Stat(filepath.Join(request.Root.Path, "hooks/nested"))
				if err != nil || !nested.IsDir() || nested.Mode().Perm() != 0750 {
					t.Fatalf("%s: nested private directory mode changed: %v %v", operation, nested, err)
				}
				contextFile, err := os.Stat(filepath.Join(request.Root.Path, "hooks/nested/context.md"))
				if err != nil || contextFile.Mode().Perm() != 0600 {
					t.Fatalf("%s: nested private file mode changed: %v %v", operation, contextFile, err)
				}
				manifest := result.Handles[0].Manifest
				found := false
				for _, entry := range manifest.Entries {
					if entry.Path == "hooks" {
						found = entry.Kind == artifact.EntryDirectory && entry.Mode == uint32(wanted)
					}
				}
				if !found {
					t.Fatalf("%s: manifest does not bind physical directory mode", operation)
				}
				generation = manifest.Generation
			}
		})
	}
}

func TestUnsafeDirectoryModesRefuseBeforeMutation(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0644, 0710, 0777, 0775, 0757, 0700 | os.ModeSetuid, 0750 | os.ModeSetgid, 0755 | os.ModeSticky} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			options, request := fixture(t)
			request.Tree = directoryTree(mode, "unchanged")
			ports, closePorts, err := local.New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer closePorts()
			result, err := workspace.ApplyTree(context.Background(), request, ports)
			var refusal *workspace.Refusal
			if !errors.As(err, &refusal) || refusal.Code != workspace.CodeUnsafeArtifactMode || result.ArtifactsComplete() || len(result.Handles) != 0 {
				t.Fatalf("unsafe directory accepted: result=%+v err=%v", result, err)
			}
			if _, err := os.Lstat(request.Root.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused directory mutated candidate: %v", err)
			}
		})
	}
}
