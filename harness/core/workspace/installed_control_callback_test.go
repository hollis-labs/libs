package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

func TestInstalledHostCallbackRevalidatesConfiguredControl(t *testing.T) {
	for _, point := range []string{"aggregate-intent", "file-intent", "committed-receipt", "unchanged-control"} {
		t.Run(point, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			p := planned(t, s, c, r, o)
			records, armed, changed := 0, false, false
			f.onRecord = func(receipt workspace.Receipt) error {
				records++
				if point == "aggregate-intent" && records == 1 || point == "file-intent" && records == 2 || point == "committed-receipt" && receipt.Phase == workspace.ArtifactsCommitted {
					armed = true
				}
				return nil
			}
			f.onValidate = func() error {
				if armed && !changed {
					changed = true
					f.control.Owner = "foreign-configured-store"
				}
				return nil
			}
			result, err := workspace.Materialize(context.Background(), p, f.ports())
			if point == "unchanged-control" {
				if err != nil || !result.ArtifactsComplete() || result.LaunchReady() {
					t.Fatal("valid installed completion changed", err)
				}
				return
			}
			if !changed {
				t.Fatal("last authority callback not reached")
			}
			if err == nil || result.ArtifactsComplete() || result.LaunchReady() {
				t.Fatal("rebound configured control earned completion")
			}
			_, readErr := os.Stat(filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path))
			if point == "committed-receipt" {
				if readErr != nil || result.Status != workspace.Partial || len(result.Retained) == 0 {
					t.Fatal("lost committed partial root")
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatal("published after configured control rebound")
			}
			if point == "aggregate-intent" && len(result.Handles) > 0 {
				t.Fatal("known refusal reached engine")
			}
			if point == "file-intent" && (result.Status != workspace.Partial || len(result.Retained) == 0) {
				t.Fatal("lost staged partial root")
			}
		})
	}
}

func TestInstalledTerminalCallbacksCannotKeepCompletion(t *testing.T) {
	for _, point := range []string{"release-control", "release-content", "clock-control", "unchanged-release"} {
		t.Run(point, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			p := planned(t, s, c, r, o)
			changed := false
			switch point {
			case "release-control":
				f.onRelease = func() error {
					if !changed {
						changed = true
						f.control.Owner = "foreign"
					}
					return nil
				}
			case "release-content":
				f.onRelease = func() error {
					if !changed {
						changed = true
						return os.WriteFile(filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path), []byte(`{"operator":2}`), 0600)
					}
					return nil
				}
			case "clock-control":
				f.onRecord = func(receipt workspace.Receipt) error {
					if receipt.Phase == workspace.ArtifactsCommitted {
						f.onClock = func() {
							if !changed {
								changed = true
								f.control.Owner = "foreign"
							}
						}
					}
					return nil
				}
			case "unchanged-release":
				f.onRelease = func() error { changed = true; return nil }
			}
			result, err := workspace.Materialize(context.Background(), p, f.ports())
			if !changed {
				t.Fatal("callback not reached")
			}
			if point == "unchanged-release" {
				if err != nil || !result.ArtifactsComplete() {
					t.Fatal("valid release changed completion", err)
				}
				return
			}
			if err == nil || result.ArtifactsComplete() || result.LaunchReady() || result.Status != workspace.Partial || len(result.Retained) == 0 {
				t.Fatal("terminal callback retained false completion", err)
			}
			if result.Receipt.Phase != workspace.Interrupted {
				t.Fatal("lost partial receipt")
			}
		})
	}
}

func TestInstalledCallbackPhysicalControlMetadataAndIdentity(t *testing.T) {
	for _, point := range []string{"file-intent", "committed-receipt"} {
		for _, change := range []string{"mode", "replacement"} {
			t.Run(point+"/"+change, func(t *testing.T) {
				s, c, r, o, f := installedInputs(t, runtimes.Claude)
				p := planned(t, s, c, r, o)
				armed, changed := false, false
				records := 0
				f.onRecord = func(receipt workspace.Receipt) error {
					if changed {
						t.Fatal("recorded recovery through changed physical control")
					}
					records++
					if point == "file-intent" && records == 2 || point == "committed-receipt" && receipt.Phase == workspace.ArtifactsCommitted {
						armed = true
					}
					return nil
				}
				f.onValidate = func() error {
					if !armed || changed {
						return nil
					}
					changed = true
					if change == "mode" {
						return os.Chmod(s.Installed.Control.Path, 0750)
					}
					if err := os.Rename(s.Installed.Control.Path, s.Installed.Control.Path+"-retained"); err != nil {
						return err
					}
					return os.Mkdir(s.Installed.Control.Path, 0700)
				}
				result, err := workspace.Materialize(context.Background(), p, f.ports())
				if !changed || err == nil || result.ArtifactsComplete() || result.Status != workspace.Partial || len(result.Retained) == 0 {
					t.Fatal("lost physical control mismatch or retained obligations", err)
				}
				_, diskErr := os.Stat(filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path))
				if point == "file-intent" && !os.IsNotExist(diskErr) {
					t.Fatal("published through changed physical control")
				}
				if point == "committed-receipt" && diskErr != nil {
					t.Fatal("removed committed target")
				}
			})
		}
	}
}

func TestInstalledClockCannotRepairInvalidHostCustody(t *testing.T) {
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	p := planned(t, s, c, r, o)
	armed, changed := false, false
	f.onRecord = func(receipt workspace.Receipt) error {
		if receipt.Phase == workspace.ArtifactsCommitted {
			armed = true
		}
		return nil
	}
	f.onValidate = func() error {
		if armed {
			changed = true
			f.control.Owner = "foreign"
		}
		return nil
	}
	f.onClock = func() {
		if armed && f.control.Owner != "" {
			f.control = s.Installed.Control
		}
	}
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if !changed || err == nil || result.ArtifactsComplete() || len(result.Retained) == 0 {
		t.Fatal("later callback repaired known invalid host custody", err)
	}
}

func TestInstalledFinalClockCallbackCannotRebindBeforePublication(t *testing.T) {
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	p := planned(t, s, c, r, o)
	armed, hostReturned, changed := false, false, false
	f.onRecord = func(receipt workspace.Receipt) error {
		if receipt.Installed != nil {
			for _, c := range receipt.Installed.Files {
				if c.Phase == materialize.InstalledIntent {
					armed = true
				}
			}
		}
		return nil
	}
	f.onValidate = func() error { hostReturned = true; return nil }
	f.onClock = func() {
		if hostReturned {
			hostReturned = false
			if armed && !changed {
				changed = true
				f.control.Owner = "foreign"
			}
		}
	}
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if !changed || err == nil || result.ArtifactsComplete() || len(result.Retained) == 0 {
		t.Fatal("clock rebind lost partial obligation", err)
	}
	if _, err := os.Stat(filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path)); !os.IsNotExist(err) {
		t.Fatal("published after final clock callback rebound control")
	}
}
