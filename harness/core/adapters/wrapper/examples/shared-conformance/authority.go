package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/local"
)

// The synthetic demo host deliberately authorizes a fresh inactive candidate.
// This is not a production default: other hosts must supply their own grants,
// custody and durable control storage. Existing control storage refuses here.
func syntheticAuthority(base, candidate string) (agentlaunch.ArtifactAuthorizer, error) {
	root := workspace.RootRef{ID: "synthetic-candidate", Path: candidate, AllowedBase: base, Owner: "synthetic-demo-host", Provenance: "synthetic-owned-resource"}
	control := workspace.RootRef{ID: "synthetic-control", Path: filepath.Join(base, "control"), AllowedBase: base, Owner: root.Owner, Provenance: root.Provenance}
	if err := os.Mkdir(control.Path, 0700); err != nil {
		return nil, err
	}
	grant := workspace.EffectGrant{Kind: workspace.ArtifactEffect, RootID: root.ID, AuthorizationID: "synthetic-demo-artifacts", Version: "1"}
	resources := workspace.Resources{Roots: []workspace.RootRef{root}, LockRoot: control, LockNamespace: control.Path, Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks}, Grants: []workspace.EffectGrant{grant}}
	sequence := 0
	return func(ctx context.Context, path string) (agentlaunch.ArtifactAuthority, error) {
		if path != root.Path {
			return agentlaunch.ArtifactAuthority{}, fmt.Errorf("synthetic host: undeclared candidate")
		}
		if err := ctx.Err(); err != nil {
			return agentlaunch.ArtifactAuthority{}, err
		}
		sequence++
		operation := fmt.Sprintf("synthetic-operation-%d", sequence)
		observe := func(ctx context.Context) (workspace.Observations, error) {
			if err := ctx.Err(); err != nil {
				return workspace.Observations{}, err
			}
			now := time.Now().UTC()
			out := workspace.Observations{At: now, ExpiresAt: now.Add(time.Minute), Capabilities: resources.Capabilities}
			for _, ref := range []workspace.RootRef{root, control} {
				o, err := workspace.InspectRoot(ref)
				if err != nil {
					return out, err
				}
				out.Roots = append(out.Roots, o)
			}
			return out, nil
		}
		observed, err := observe(ctx)
		if err != nil {
			return agentlaunch.ArtifactAuthority{}, err
		}
		ports, closePorts, err := local.New(local.Options{OperationID: operation, ControlRoot: control, Resources: resources, LocalFilesystem: true, ValidateAuthority: func(ctx context.Context, s workspace.Spec, r workspace.Resources) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !reflect.DeepEqual(r, resources) || s.OperationID != operation {
				return fmt.Errorf("synthetic host: authority changed")
			}
			return nil
		}, Evidence: observe})
		if err != nil {
			return agentlaunch.ArtifactAuthority{}, err
		}
		input := workspace.TreeRequest{OperationID: operation, Root: root, RootMode: 0700, Resources: resources, Observed: observed}
		if observed.Roots[0].Manifest != nil {
			input.ExpectedGeneration = observed.Roots[0].Manifest.Generation
		}
		return agentlaunch.ArtifactAuthority{Inactive: true, PrivateCustody: true, Input: input, Ports: ports, Close: closePorts}, nil
	}, nil
}
