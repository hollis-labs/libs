package workspace_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/publication"
)

func TestEarlierUncertainPinSurvivesSuccessfulArtifactOnlyAttempt(t *testing.T) {
	first, f, s := applyFixture(t)
	r := first.Resources()
	hash := sha256.Sum256([]byte(s.Boot.IdentityRoot.Path))
	// Explicit synthetic trusted earlier control evidence; no actual inode,
	// use/adoption/metadata/absence capability or launch readiness is asserted.
	e := workspace.PinCreationEvidence{Version: workspace.PinCreationVersion, Origin: publication.Origin{OperationID: s.OperationID, InputDigest: first.Digest(), AgentURN: s.Identity.AgentURN, IdentityKey: s.Identity.EncodedKey}, Control: r.LockRoot, Key: workspace.LockKey{Namespace: r.LockNamespace, CanonicalID: s.Boot.IdentityRoot.Path}, Path: filepath.Join(r.LockNamespace, "pin-"+hex.EncodeToString(hash[:])), JournalID: "original-journal", ReservationID: "original-reservation", Grant: workspace.EffectGrant{Kind: workspace.PinCreationEffect, RootID: s.Boot.IdentityRoot.ID, AuthorizationID: "original-explicit-grant", Version: "1"}, Uncertain: true}
	obligation := workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: s.Boot.IdentityRoot.ID, Code: "earlier-uncertain-pin"}
	r.RecoveryReceipts = []workspace.Receipt{{SchemaVersion: workspace.SchemaVersion, OperationID: s.OperationID, InputDigest: first.Digest(), IdentityKey: s.Identity.EncodedKey, Identity: s.Identity, Phase: workspace.Interrupted, PinCreation: &e, Obligations: []workspace.Obligation{obligation}}}
	s.OperationID = "later-artifact-attempt"
	_, c, _, _ := planInputs(t)
	for key, path := range c.Roots {
		if filepath.Base(path) == "home" {
			c.Roots[key] = s.Home.Root.Path
		} else if filepath.Base(path) == "candidate" {
			c.Roots[key] = s.Boot.Candidate.Path
		}
	}
	c.Rendered[0].Binding.Argv = []string{"--add-dir", s.Home.Root.Path}
	c.Rendered[0].Binding.CWD = s.Boot.Candidate.Path
	p, err := workspace.Plan(s, c, r, f.observed)
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if err != nil || !result.ArtifactsComplete() || result.LaunchReady() || !slices.Contains(result.Obligations, obligation) || !slices.Contains(result.Retained, s.Boot.IdentityRoot) || len(result.Receipt.PinOrigins) != 1 || result.Receipt.PinOrigins[0].Evidence != e || result.Receipt.PinOrigins[0].OperationID != e.Origin.OperationID || result.Receipt.PinOrigins[0].InputDigest != e.Origin.InputDigest {
		t.Fatalf("new artifact success reset original pin uncertainty: %+v %v", result, err)
	}
}
