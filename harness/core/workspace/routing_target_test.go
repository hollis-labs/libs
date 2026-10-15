package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/materialize"
)

func TestRoutingExistingEmptyRootRequiresPrivateMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "candidate")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	a := Action{Root: RootRef{Path: root}, RootMode: 0700, Request: materialize.Request{Operation: materialize.OperationCreate, ExistingTarget: materialize.ExistingTargetAllowEmpty}}
	err := validateTarget(a, RootObservation{Exists: true, Directory: true, Empty: true}, nil)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUnsafeCandidateMode {
		t.Fatalf("unsafe existing root accepted: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("existing root changed: %v %v", info, err)
	}
}

func TestRoutingRefreshValidatesCommittedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "candidate")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	m := materialize.Manifest{Generation: "prior"}
	a := Action{Root: RootRef{Path: root}, RootMode: 0700, Request: materialize.Request{Operation: materialize.OperationRefresh, ExpectedGeneration: "prior", CurrentManifest: &m}}
	if err := validateTarget(a, RootObservation{Exists: true, Directory: true, Manifest: &m}, nil); err != nil {
		t.Fatalf("committed refresh refused: %v", err)
	}
	changed := m
	changed.Generation = "changed"
	if err := validateTarget(a, RootObservation{Exists: true, Directory: true, Manifest: &changed}, nil); err == nil {
		t.Fatal("stale refresh accepted")
	}
}
