//go:build linux || darwin

package workspace_test

import (
	"context"
	"slices"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

func TestRootRepositoryOriginRejectsCoherentSplice(t *testing.T) {
	for _, kind := range []string{"operation", "digest", "inherited-operation", "inherited-digest"} {
		t.Run(kind, func(t *testing.T) {
			s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
			first, err := workspace.Materialize(context.Background(), planned(t, s, c, r, f.observed), repositoryPorts(f, store, tp, rp))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "inherited-operation" || kind == "inherited-digest" {
				s.OperationID = "origin-intermediate"
				s.Boot.CandidateGeneration = first.Handles[0].Manifest.Generation
				r.RecoveryReceipts = []workspace.Receipt{first.Receipt}
				refreshRepositoryObservations(t, r, f)
				first, err = workspace.Materialize(context.Background(), planned(t, s, c, r, f.observed), repositoryPorts(f, store, tp, rp))
				if err != nil {
					t.Fatal(err)
				}
			}
			original := first.Receipt.RepositoryRequests[0]
			if kind != "inherited-operation" && kind != "inherited-digest" && (original.Header.OperationID != first.Receipt.OperationID || original.Header.InputDigest != first.Receipt.InputDigest) {
				t.Fatal("invalid originating receipt control")
			}
			if kind == "operation" || kind == "inherited-operation" {
				first.Receipt.RepositoryRequests[0].Header.OperationID = "foreign-original-operation"
			} else {
				first.Receipt.RepositoryRequests[0].Header.InputDigest = "foreign-original-digest"
			}
			for i := range first.Receipt.EffectEvidence {
				if first.Receipt.EffectEvidence[i].Kind == effects.RepositoryAttachment {
					first.Receipt.EffectEvidence[i].Header = first.Receipt.RepositoryRequests[0].Header
				}
			}
			obligation := workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: "repo-parent", Code: "valid-origin-obligation"}
			first.Receipt.Obligations = append(first.Receipt.Obligations, obligation)
			s.OperationID = "origin-retry"
			s.Boot.CandidateGeneration = first.Handles[0].Manifest.Generation
			r.RecoveryReceipts = []workspace.Receipt{first.Receipt}
			refreshRepositoryObservations(t, r, f)
			f.events = nil
			store.records = nil
			rp.observedRequests = nil
			got, err := workspace.Materialize(context.Background(), planned(t, s, c, r, f.observed), repositoryPorts(f, store, tp, rp))
			if !slices.Contains(got.Obligations, obligation) {
				t.Fatal("lost trusted outer obligation on origin refusal")
			}
			if err == nil || got.ArtifactsComplete() || len(store.records) > 0 || len(rp.observedRequests) > 0 || slices.Contains(f.events, "repo:create") || slices.Contains(f.events, "trust:begin") {
				t.Fatalf("coherent %s splice reached inspection/receipts/mutation: err=%v artifacts=%v records=%d inspections=%d", kind, err, got.ArtifactsComplete(), len(store.records), len(rp.observedRequests))
			}
		})
	}
}

func TestRootRepositoryOriginSurvivesMultipleOperations(t *testing.T) {
	s, c, r, f, store, tp, rp := repositoryApplyInputs(t)
	first, err := workspace.Materialize(context.Background(), planned(t, s, c, r, f.observed), repositoryPorts(f, store, tp, rp))
	if err != nil {
		t.Fatal(err)
	}
	original := first.Receipt.RepositoryRequests[0]
	obligation := workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: "repo-parent", Code: "origin-retained"}
	first.Receipt.Obligations = append(first.Receipt.Obligations, obligation)
	prior := first
	for _, operation := range []string{"origin-second", "origin-third", "origin-fourth"} {
		s.OperationID = operation
		s.Boot.CandidateGeneration = prior.Handles[0].Manifest.Generation
		r.RecoveryReceipts = []workspace.Receipt{prior.Receipt}
		refreshRepositoryObservations(t, r, f)
		f.events = nil
		store.records = nil
		rp.observedRequests = nil
		next, e := workspace.Materialize(context.Background(), planned(t, s, c, r, f.observed), repositoryPorts(f, store, tp, rp))
		if e != nil || !next.ArtifactsComplete() || next.LaunchReady() || !slices.Contains(next.Obligations, obligation) {
			t.Fatal("multi-operation origin/obligation lost", operation, e, next.Obligations)
		}
		if len(rp.observedRequests) != 1 || rp.observedRequests[0] != original || slices.Contains(f.events, "repo:create") || slices.Contains(f.events, "repo:base") {
			t.Fatal("multi-operation original altered/replayed", operation, rp.observedRequests, f.events)
		}
		if original.Header.OperationID == next.Receipt.OperationID || original.Header.InputDigest == next.Receipt.InputDigest {
			t.Fatal("control must use older origin than enclosing aggregate")
		}
		prior = next
	}
}
