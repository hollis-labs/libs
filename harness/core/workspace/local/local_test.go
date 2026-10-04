//go:build linux || darwin

package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/local"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

func fixture(t *testing.T) (local.Options, workspace.TreeRequest) {
	t.Helper()
	base := t.TempDir()
	control := workspace.RootRef{ID: "control", Path: filepath.Join(base, "control"), AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
	if err := os.Mkdir(control.Path, 0700); err != nil {
		t.Fatal(err)
	}
	root := workspace.RootRef{ID: "candidate", Path: filepath.Join(base, "candidate"), AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
	resources := workspace.Resources{Roots: []workspace.RootRef{root}, LockNamespace: control.Path, Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks}, Grants: []workspace.EffectGrant{{Kind: workspace.ArtifactEffect, RootID: root.ID, AuthorizationID: "fixture", Version: "1"}}}
	now := time.Now().UTC()
	observed := workspace.Observations{At: now, ExpiresAt: now.Add(time.Minute), Capabilities: resources.Capabilities}
	o, err := workspace.InspectRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	observed.Roots = []workspace.RootObservation{o}
	options := local.Options{OperationID: "fixture-operation", ControlRoot: control, Resources: resources, LocalFilesystem: true, ValidateAuthority: func(context.Context, workspace.Spec, workspace.Resources) error { return nil }, Evidence: func(context.Context) (workspace.Observations, error) { return observed, nil }}
	request := workspace.TreeRequest{OperationID: options.OperationID, Root: root, RootMode: 0700, Resources: resources, Observed: observed, Tree: artifact.Tree{Entries: []artifact.Entry{{Path: "AGENTS.md", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("fixture"), Ownership: artifact.Ownership{EntryID: "fixture", GroupID: "fixture"}, Provenance: artifact.Provenance{Source: "fixture"}}}}}
	return options, request
}
func TestOptInLocalApplyAndCredentialPreservingRefresh(t *testing.T) {
	options, request := fixture(t)
	ports, closePorts, err := local.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer closePorts()
	result, err := workspace.ApplyTree(context.Background(), request, ports)
	if err != nil || !result.ArtifactsComplete() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	auth := filepath.Join(request.Root.Path, "auth.json")
	if err := os.WriteFile(auth, []byte("fixture-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("auth.json", filepath.Join(request.Root.Path, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	// A refresh is a separately authorized operation on a committed inactive root.
	closePorts()
	options.OperationID = "fixture-refresh"
	request.OperationID = options.OperationID
	request.ExpectedGeneration = result.Handles[0].Manifest.Generation
	request.Tree.Entries[0].Bytes = []byte("updated")
	o, err := workspace.InspectRoot(request.Root)
	if err != nil {
		t.Fatal(err)
	}
	request.Observed.Roots = []workspace.RootObservation{o}
	ports, closeNew, err := local.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer closeNew()
	result, err = workspace.ApplyTree(context.Background(), request, ports)
	if err != nil || !result.ArtifactsComplete() {
		t.Fatalf("refresh=%+v err=%v", result, err)
	}
	b, err := os.ReadFile(auth)
	if err != nil || string(b) != "fixture-sentinel" {
		t.Fatal("credential changed", err)
	}
	target, err := os.Readlink(filepath.Join(request.Root.Path, ".credentials.json"))
	if err != nil || target != "auth.json" {
		t.Fatal("credential link changed", err)
	}
}
func TestLocalLockCancellationAndOperationBinding(t *testing.T) {
	options, request := fixture(t)
	ports, closePorts, err := local.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer closePorts()
	key := workspace.LockKey{Namespace: options.ControlRoot.Path, CanonicalID: request.Root.Path}
	held, err := ports.Locks.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = ports.Locks.Acquire(ctx, key)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("contended acquire did not cancel", err)
	}
	if err = held.Release(); err != nil {
		t.Fatal(err)
	}
	held, err = ports.Locks.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	held.Release()
	receipt := workspace.Receipt{SchemaVersion: workspace.SchemaVersion, OperationID: options.OperationID, InputDigest: "fixture-digest", Phase: workspace.Planned}
	if err := ports.ReceiptStore.Record(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	receipt.InputDigest = "different"
	if err := ports.ReceiptStore.Record(context.Background(), receipt); err == nil {
		t.Fatal("operation ID rebound")
	}
}
func TestLocalRefusesAmbientAuthorityAndOverlappingControl(t *testing.T) {
	options, request := fixture(t)
	for _, kind := range []string{"authority", "filesystem", "overlap", "privacy"} {
		t.Run(kind, func(t *testing.T) {
			o := options
			switch kind {
			case "authority":
				o.ValidateAuthority = nil
			case "filesystem":
				o.LocalFilesystem = false
			case "overlap":
				o.ControlRoot = request.Root
				os.Mkdir(request.Root.Path, 0700)
				o.Resources.LockNamespace = request.Root.Path
			case "privacy":
				os.Chmod(options.ControlRoot.Path, 0755)
			}
			_, closePorts, err := local.New(o)
			if err == nil {
				closePorts()
				t.Fatal("unsafe local options accepted")
			}
		})
	}
}

func TestRefreshRefusesManagedSymlinkAndStaleGeneration(t *testing.T) {
	for _, kind := range []string{"symlink", "stale generation"} {
		t.Run(kind, func(t *testing.T) {
			options, request := fixture(t)
			ports, closePorts, err := local.New(options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := workspace.ApplyTree(context.Background(), request, ports)
			if err != nil {
				t.Fatal(err)
			}
			closePorts()
			options.OperationID = "fixture-refresh"
			request.OperationID = options.OperationID
			request.ExpectedGeneration = result.Handles[0].Manifest.Generation
			request.Tree.Entries[0].Bytes = []byte("updated")
			if kind == "symlink" {
				sentinel := filepath.Join(request.Root.AllowedBase, "sentinel")
				os.WriteFile(sentinel, []byte("fixture-sentinel"), 0600)
				os.Remove(filepath.Join(request.Root.Path, "AGENTS.md"))
				os.Symlink(sentinel, filepath.Join(request.Root.Path, "AGENTS.md"))
			} else {
				request.ExpectedGeneration = "stale"
			}
			o, err := workspace.InspectRoot(request.Root)
			if err != nil {
				t.Fatal(err)
			}
			request.Observed.Roots = []workspace.RootObservation{o}
			ports, closeNew, err := local.New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer closeNew()
			got, err := workspace.ApplyTree(context.Background(), request, ports)
			if err == nil || got.ArtifactsComplete() {
				t.Fatal("unsafe refresh accepted")
			}
			if kind == "symlink" {
				b, _ := os.ReadFile(filepath.Join(request.Root.AllowedBase, "sentinel"))
				if string(b) != "fixture-sentinel" {
					t.Fatal("outside sentinel changed")
				}
			}
		})
	}
}
func TestEmptyTreeLeavesNoRootOrManifest(t *testing.T) {
	options, request := fixture(t)
	request.Tree = artifact.Tree{}
	ports, closePorts, err := local.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer closePorts()
	got, err := workspace.ApplyTree(context.Background(), request, ports)
	if err != nil || !got.ArtifactsComplete() || len(got.Handles) != 0 {
		t.Fatal("empty artifact tree failed", err)
	}
	if _, err := os.Stat(request.Root.Path); !os.IsNotExist(err) {
		t.Fatal("empty tree created root", err)
	}
}

func TestExplicitCredentialDestinationCannotBecomeArtifact(t *testing.T) {
	options, request := fixture(t)
	request.CredentialDestinations = []string{"private/token"}
	request.Tree.Entries[0].Path = "private/token"
	ports, closePorts, err := local.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer closePorts()
	got, err := workspace.ApplyTree(context.Background(), request, ports)
	if err == nil || got.ArtifactsComplete() {
		t.Fatal("credential exclusion bypassed")
	}
	if _, err := os.Stat(request.Root.Path); !os.IsNotExist(err) {
		t.Fatal("credential exclusion mutated root", err)
	}
}
