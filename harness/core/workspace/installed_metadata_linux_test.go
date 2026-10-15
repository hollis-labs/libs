//go:build linux

package workspace_test

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledMetadataRefusesBeforeReceiptOrStage(t *testing.T) {
	for _, kind := range []string{"hardlink", "xattr", "mode"} {
		t.Run(kind, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			leaf := filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path)
			if err := os.WriteFile(leaf, []byte(`{"operator":1}`), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "hardlink":
				if err := os.Link(leaf, filepath.Join(s.Installed.Target.Path, "unmanaged-link")); err != nil {
					t.Fatal(err)
				}
			case "xattr":
				if err := unix.Setxattr(leaf, "user.fixture", []byte("opaque fixture"), 0); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(leaf, 0644); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			o.InstalledFiles, err = materialize.InspectInstalled(context.Background(), s.Installed.Target.Path, c.Rendered[0].Tree, o.InstalledCaseMode)
			if err == nil {
				var p workspace.PlannedWorkspace
				p, err = workspace.Plan(s, c, r, o)
				if err == nil {
					f.observed = o
					_, err = workspace.Materialize(context.Background(), p, f.ports())
				}
			}
			var refusal *workspace.Refusal
			if !errors.Is(err, materialize.ErrUnsupportedOperation) && (!errors.As(err, &refusal) || refusal.Status != workspace.Unsupported) {
				t.Fatalf("metadata was not refused as unsupported: %v", err)
			}
			if len(f.records) != 0 {
				t.Fatal("recorded before metadata refusal")
			}
			raw, _ := os.ReadFile(leaf)
			if string(raw) != `{"operator":1}` {
				t.Fatal("changed operator content")
			}
			names, _ := os.ReadDir(s.Installed.Target.Path)
			for _, n := range names {
				if len(n.Name()) >= 17 && n.Name()[:17] == ".installed-stage-" {
					t.Fatal("staged before metadata refusal")
				}
			}
		})
	}
}

func TestInstalledMetadataCallbacksRetainBeforeAndAfterPublication(t *testing.T) {
	for _, point := range []string{"leaf-intent", "stage-intent", "temp-intent", "leaf-committed"} {
		t.Run(point, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			p := planned(t, s, c, r, o)
			path := filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path)
			armed, changed := false, false
			stage := ""
			f.onRecord = func(receipt workspace.Receipt) error {
				if receipt.Installed == nil {
					return nil
				}
				for _, change := range receipt.Installed.Files {
					if change.Phase == materialize.InstalledIntent {
						stage = receipt.Installed.Stage.Path
						if point != "leaf-committed" {
							armed = true
						}
					}
				}
				if point == "leaf-committed" && receipt.Phase == workspace.ArtifactsCommitted {
					armed = true
				}
				return nil
			}
			f.onValidate = func() error {
				if !armed || changed {
					return nil
				}
				changed = true
				switch point {
				case "leaf-intent":
					return os.WriteFile(path, []byte(`{"operator":2}`), 0644)
				case "stage-intent":
					return unix.Setxattr(filepath.Join(s.Installed.Target.Path, stage), "user.fixture", []byte("opaque fixture"), 0)
				case "temp-intent":
					return unix.Setxattr(filepath.Join(s.Installed.Target.Path, stage, "file-0"), "user.fixture", []byte("opaque fixture"), 0)
				case "leaf-committed":
					return os.Chmod(path, 0640)
				}
				return nil
			}
			result, err := workspace.Materialize(context.Background(), p, f.ports())
			if !changed || err == nil || result.ArtifactsComplete() || result.Status != workspace.Partial || len(result.Retained) == 0 {
				t.Fatal("lost changed metadata partial obligation", err)
			}
			if point == "leaf-intent" {
				raw, _ := os.ReadFile(path)
				if string(raw) != `{"operator":2}` {
					t.Fatal("overwrote callback operator content")
				}
			}
			if point == "stage-intent" || point == "temp-intent" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("published with changed stage metadata")
				}
				if _, err := os.Stat(filepath.Join(s.Installed.Target.Path, stage)); err != nil {
					t.Fatal("cleared uncertain stage")
				}
			}
			if point == "leaf-committed" {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0640 {
					t.Fatal("undid later user metadata")
				}
			}
		})
	}
}

func TestInstalledObservedMetadataMismatchCannotBeErasedByLaterCallback(t *testing.T) {
	// The author-only SYNTHETIC metadata overlay maps this owned fixture marker to
	// a different observed UID. Production never interprets or allows the marker.
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	p := planned(t, s, c, r, o)
	leaf := filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path)
	committed := false
	callbacks := 0
	f.onRecord = func(receipt workspace.Receipt) error {
		if receipt.Phase == workspace.ArtifactsCommitted {
			committed = true
		}
		return nil
	}
	f.onValidate = func() error {
		if !committed {
			return nil
		}
		callbacks++
		if callbacks == 1 {
			return unix.Setxattr(leaf, "user.fixture.synthetic-owner", nil, 0)
		}
		if callbacks == 2 {
			return unix.Removexattr(leaf, "user.fixture.synthetic-owner")
		}
		return nil
	}
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if callbacks < 2 || err == nil || result.ArtifactsComplete() || len(result.Retained) == 0 {
		t.Fatal("later callback erased an already observed metadata mismatch", err)
	}
}
