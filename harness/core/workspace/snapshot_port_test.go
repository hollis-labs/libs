package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
)

func TestSnapshotPortDoesNotAuthorizeAutomaticCapture(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "supplied"
		}
		t.Run(name, func(t *testing.T) {
			plan, fixture, _ := applyFixture(t)
			ports := fixture.ports()
			if ports.Snapshots != nil {
				t.Fatal("snapshot capture enabled by default")
			}
			if enabled {
				ports.Snapshots = forbiddenAutomaticSnapshot{}
			}
			result, err := workspace.Materialize(context.Background(), plan, ports)
			if err != nil || !result.ArtifactsComplete() || result.LaunchReady() {
				t.Fatalf("ordinary materialization changed: status=%s err=%v", result.Status, err)
			}
		})
	}
}

type forbiddenAutomaticSnapshot struct{}

func (forbiddenAutomaticSnapshot) Capture(context.Context, []snapshot.Target) (snapshot.SnapshotSet, error) {
	panic("automatic capture")
}
func (forbiddenAutomaticSnapshot) Diff(context.Context, snapshot.SnapshotSet, snapshot.SnapshotSet) (snapshot.Diff, error) {
	panic("automatic diff")
}
func (forbiddenAutomaticSnapshot) Preview(context.Context, snapshot.SnapshotSet, []string) (snapshot.Preview, error) {
	panic("automatic preview")
}
func (forbiddenAutomaticSnapshot) Restore(context.Context, snapshot.SnapshotSet, []string) error {
	panic("automatic restore")
}

func TestSnapshotPortDispatchesExplicitOwnedFixtureOperations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	file := filepath.Join(root, "example.txt")
	if err := os.WriteFile(file, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := snapshot.NewShadowGit(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ports := workspace.Ports{Snapshots: provider}
	targets := []snapshot.Target{{ID: "owned-fixture", Root: root}}
	before, err := ports.Snapshots.Capture(ctx, targets)
	if err != nil {
		t.Fatal(err)
	}
	if before.Roots[targets[0].ID].Err != nil || before.Roots[targets[0].ID].CommitHash == "" {
		t.Fatal("initial fixture capture incomplete")
	}
	if err := os.WriteFile(file, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := ports.Snapshots.Capture(ctx, targets)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := ports.Snapshots.Diff(ctx, before, after)
	if err != nil || len(diff.Roots[targets[0].ID].Files) != 1 {
		t.Fatalf("diff=%+v err=%v", diff, err)
	}
	paths := []string{snapshot.JoinPath(targets[0].ID, "example.txt")}
	preview, err := ports.Snapshots.Preview(ctx, before, paths)
	if err != nil || len(preview.Roots[targets[0].ID].Changes) != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if err := ports.Snapshots.Restore(ctx, before, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "after" {
		t.Fatal("empty restore changed fixture", err)
	}
	if err := ports.Snapshots.Restore(ctx, before, paths); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(file)
	if err != nil || string(data) != "before" {
		t.Fatal("explicit restore did not dispatch", err)
	}
}
