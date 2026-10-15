// Package workspacetest supplies explicit test-owned workspace authority. It
// never authorizes a production resource or changes existing directory modes.
package workspacetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/local"
)

type Authorizer func(context.Context, string) (workspace.TreeRequest, workspace.Ports, func() error, error)

func New(t testing.TB) Authorizer {
	t.Helper()
	controlBase := t.TempDir()
	control := workspace.RootRef{ID: "control", Path: filepath.Join(controlBase, "control"), AllowedBase: controlBase, Owner: "fixture", Provenance: "test-owned"}
	if err := os.Mkdir(control.Path, 0700); err != nil {
		t.Fatal(err)
	}
	sequence := 0
	return func(ctx context.Context, path string) (workspace.TreeRequest, workspace.Ports, func() error, error) {
		sequence++
		root := workspace.RootRef{ID: "candidate", Path: path, AllowedBase: filepath.Dir(path), Owner: "fixture", Provenance: "test-owned"}
		resources := workspace.Resources{Roots: []workspace.RootRef{root}, LockRoot: control, LockNamespace: control.Path, Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks}, Grants: []workspace.EffectGrant{{Kind: workspace.ArtifactEffect, RootID: root.ID, AuthorizationID: "test-owned", Version: "1"}}}
		operation := fmt.Sprintf("fixture-operation-%d", sequence)
		observe := func(context.Context) (workspace.Observations, error) {
			now := time.Now().UTC()
			observed := workspace.Observations{At: now, ExpiresAt: now.Add(time.Minute), Capabilities: resources.Capabilities}
			for _, r := range []workspace.RootRef{root, control} {
				o, err := workspace.InspectRoot(r)
				if err != nil {
					return observed, err
				}
				observed.Roots = append(observed.Roots, o)
			}
			return observed, nil
		}
		observed, err := observe(ctx)
		if err != nil {
			return workspace.TreeRequest{}, workspace.Ports{}, nil, err
		}
		ports, closePorts, err := local.New(local.Options{OperationID: operation, ControlRoot: control, Resources: resources, LocalFilesystem: true, ValidateAuthority: func(ctx context.Context, _ workspace.Spec, _ workspace.Resources) error { return ctx.Err() }, Evidence: observe})
		if err != nil {
			return workspace.TreeRequest{}, workspace.Ports{}, nil, err
		}
		input := workspace.TreeRequest{OperationID: operation, Root: root, RootMode: 0700, Resources: resources, Observed: observed}
		if observed.Roots[0].Manifest != nil {
			input.ExpectedGeneration = observed.Roots[0].Manifest.Generation
		}
		return input, ports, closePorts, nil
	}
}

// PrivateDir creates an explicit private fixture resource, without changing
// the permissions of a resource that already exists.
func PrivateDir(t testing.TB) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
