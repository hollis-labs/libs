package agentlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

type routingStore struct {
	workspace.ReceiptStore
	records int
}

func (s *routingStore) Record(ctx context.Context, r workspace.Receipt) error {
	s.records++
	return s.ReceiptStore.Record(ctx, r)
}

func TestLegacyRoutingRefusesBeforeAnyReceiptOrMutation(t *testing.T) {
	for _, name := range []string{"missing authority", "directory 0750", "reserved credential", "existing credential", "existing root 0755", "unmanifested content", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			base := fixturePrivateDir(t)
			root := filepath.Join(base, "candidate")
			tree := artifact.Tree{Entries: []artifact.Entry{{Path: "file.txt", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("desired")}}}
			var existingMode os.FileMode
			switch name {
			case "directory 0750":
				tree.Entries = []artifact.Entry{{Path: "empty", Kind: artifact.EntryDirectory, Mode: 0750}}
			case "existing credential":
				tree.Entries[0].Path = "auth.json"
				existingMode = 0700
			case "reserved credential":
				tree.Entries[0].Path = "auth.json"
			case "existing root 0755":
				existingMode = 0755
			case "unmanifested content":
				existingMode = 0700
			}
			if existingMode != 0 {
				if err := os.Mkdir(root, existingMode); err != nil {
					t.Fatal(err)
				}
			}
			if name == "unmanifested content" {
				if err := os.WriteFile(filepath.Join(root, "operator.txt"), []byte("retain"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if name == "existing credential" {
				if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte("fixture-credential-sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			authorize := fixtureAuthorization(t)
			store := &routingStore{}
			tracked := func(ctx context.Context, path string) (ArtifactAuthority, error) {
				a, err := authorize(ctx, path)
				if err == nil {
					store.ReceiptStore = a.Ports.ReceiptStore
					a.Ports.ReceiptStore = store
				}
				return a, err
			}
			req := ArtifactMaterializationRequest{TargetRoot: root, Artifacts: tree, Authorize: tracked}
			if name == "missing authority" {
				req.Authorize = nil
			}
			ctx := context.Background()
			if name == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			h, err := MaterializeArtifacts(ctx, req)
			if err == nil || h != nil || store.records != 0 {
				t.Fatalf("refusal crossed boundary: handle=%v error=%v receipts=%d", h, err, store.records)
			}
			if name != "cancelled" {
				var r *workspace.Refusal
				if !errors.As(err, &r) {
					t.Fatalf("untyped refusal: %v", err)
				}
			}
			if existingMode == 0 {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("candidate created: %v", err)
				}
			} else {
				info, err := os.Stat(root)
				if err != nil || info.Mode().Perm() != existingMode {
					t.Fatalf("candidate mode changed: %v %v", info, err)
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if name == "unmanifested content" {
					b, err := os.ReadFile(filepath.Join(root, "operator.txt"))
					if err != nil || string(b) != "retain" || len(entries) != 1 {
						t.Fatal("operator content changed")
					}
				} else if name == "existing credential" {
					b, e := os.ReadFile(filepath.Join(root, "auth.json"))
					if e != nil || string(b) != "fixture-credential-sentinel" || len(entries) != 1 {
						t.Fatal("credential content changed")
					}
				} else if len(entries) != 0 {
					t.Fatalf("refusal wrote entries: %v", entries)
				}
			}
		})
	}
}
