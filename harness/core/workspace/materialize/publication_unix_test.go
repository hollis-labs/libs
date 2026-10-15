//go:build linux || darwin

package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"github.com/hollis-labs/substrate/harness/workspace/publication"
)

type publicationFixtureHost struct {
	control  publication.Root
	records  []publication.Journal
	hook     func(publication.Journal) error
	validate func(publication.Journal) error
}

func (h *publicationFixtureHost) ValidatePublication(_ context.Context, req PublishRequest) error {
	if h.validate != nil {
		return h.validate(req.Journal.Clone())
	}
	return nil
}
func (h *publicationFixtureHost) PublicationControl() publication.Root { return h.control }
func (h *publicationFixtureHost) RecordPublication(_ context.Context, j publication.Journal) error {
	h.records = append(h.records, j.Clone())
	if h.hook != nil {
		return h.hook(j.Clone())
	}
	return nil
}

func publicationFixture(t *testing.T, existing bool) (PublishRequest, *publicationFixtureHost, *publicationCustody) {
	t.Helper()
	base := t.TempDir()
	key, err := bootkey.Encode("urn:fixture:publisher")
	if err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(base, key)
	controlPath := filepath.Join(base, "control")
	for _, dir := range []string{parentPath, controlPath} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	identity := func(path string) publication.FileIdentity {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		s := info.Sys().(*syscall.Stat_t)
		return publication.FileIdentity{Volume: "SYNTHETIC-fixture-volume", Device: uint64(s.Dev), Inode: s.Ino}
	}
	root := func(id, path string) publication.Root {
		return publication.Root{ID: id, Path: path, Owner: "fixture", Provenance: "fixture"}
	}
	parent := root("parent", parentPath)
	parent.Identity = identity(parent.Path)
	control := root("control", controlPath)
	control.Identity = identity(control.Path)
	current, candidate, aside := root("current", filepath.Join(parentPath, "current")), root("candidate", filepath.Join(parentPath, "candidate")), root("aside", filepath.Join(parentPath, "aside-original"))
	manifest := func(ref *publication.Root, generation string) string {
		if err := os.Mkdir(ref.Path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := SaveManifest(ref.Path, Manifest{SchemaVersion: "fixture", Generation: generation}); err != nil {
			t.Fatal(err)
		}
		ref.Identity = identity(ref.Path)
		bytes, err := os.ReadFile(ManifestPath(ref.Path))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(bytes)
		return hex.EncodeToString(hash[:])
	}
	newDigest := manifest(&candidate, "candidate-generation")
	oldDigest := ""
	if existing {
		oldDigest = manifest(&current, "current-generation")
	}
	hash := sha256.Sum256([]byte(parent.Path))
	lockPath := filepath.Join(controlPath, "lock-"+hex.EncodeToString(hash[:]))
	pinPath := filepath.Join(controlPath, "pin-"+hex.EncodeToString(hash[:]))
	for _, name := range []string{lockPath, pinPath} {
		if err := os.WriteFile(name, []byte("fixture-inode"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	l := publication.Layout{Parent: parent, Current: current, Candidate: candidate, Aside: aside, CurrentExists: existing, CandidateGeneration: "candidate-generation", CandidateManifestDigest: newDigest}
	if existing {
		l.ExpectedCurrentGeneration = "current-generation"
		l.ExpectedCurrentManifestDigest = oldDigest
	}
	j := publication.Journal{Version: publication.Version, JournalID: "original-journal", Control: control, Origin: publication.Origin{OperationID: "original-operation", InputDigest: hex.EncodeToString(hash[:]), AgentURN: "urn:fixture:publisher", IdentityKey: key}, Layout: l, Use: publication.UseBinding{Namespace: controlPath, CanonicalID: parent.Path, MutationPath: lockPath, PinPath: pinPath, MutationIdentity: identity(lockPath), PinIdentity: identity(pinPath), ReservationID: "fixture-reservation"}, Events: []publication.Event{{Sequence: 1, Phase: publication.Planned}, {Sequence: 2, Phase: publication.CandidateVerified, ObservedIdentity: candidate.Identity, ManifestDigest: newDigest}, {Sequence: 3, Phase: publication.ReservationHeld}}}
	if err := j.Validate(); err != nil {
		t.Fatal(err)
	}
	req := PublishRequest{Journal: j}
	host := &publicationFixtureHost{control: control}
	// Synthetic downstream admission ONLY. No actual metadata producer, use
	// reservation, absence capability, real shim adoption or Ready is exercised.
	return req, host, &publicationCustody{original: j.Clone()}
}

func TestPublicPublicationRefusesUnavailableNativeCapabilityBeforeRecord(t *testing.T) {
	req, host, _ := publicationFixture(t, true)
	out, err := NewEngine(EngineOptions{}).Publish(context.Background(), req, host)
	if !errors.Is(err, ErrUnsupportedOperation) || out.Mutated || out.Committed || len(host.records) != 0 {
		t.Fatalf("unknown metadata acquired authority/effects: %+v %v", out, err)
	}
	for _, path := range []string{req.Journal.Layout.Current.Path, req.Journal.Layout.Candidate.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(req.Journal.Layout.Aside.Path); !os.IsNotExist(err) {
		t.Fatal("unsupported publication created aside")
	}
}

func TestSyntheticPublicationTwoRenamesAndInitialAbsentCurrent(t *testing.T) {
	for _, existing := range []bool{true, false} {
		req, host, capability := publicationFixture(t, existing)
		out, err := NewEngine(EngineOptions{}).publishVerified(context.Background(), req, host, capability)
		if err != nil || !out.Mutated || !out.Committed {
			t.Fatalf("synthetic downstream: %+v %v", out, err)
		}
		manifest, err := LoadManifest(req.Journal.Layout.Current.Path)
		if err != nil || manifest.Generation != "candidate-generation" {
			t.Fatal("current not verified candidate", err)
		}
		if _, err := os.Stat(req.Journal.Layout.Candidate.Path); !os.IsNotExist(err) {
			t.Fatal("candidate remained or copied")
		}
		if existing {
			manifest, err := LoadManifest(req.Journal.Layout.Aside.Path)
			if err != nil || manifest.Generation != "current-generation" {
				t.Fatal("old current not retained aside", err)
			}
		} else if _, err := os.Stat(req.Journal.Layout.Aside.Path); !os.IsNotExist(err) {
			t.Fatal("initial absence invented old current")
		}
		if out.Journal.Events[len(out.Journal.Events)-1].Phase != publication.PublicationCommitted {
			t.Fatal("commit phase missing")
		}
	}
}

func TestSyntheticPublicationReceiptAndLateControlFailureWindows(t *testing.T) {
	for _, kind := range []string{"first-intent", "second-intent", "visible-current", "final-record", "late-control", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			req, host, capability := publicationFixture(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			host.hook = func(j publication.Journal) error {
				phase := j.Events[len(j.Events)-1].Phase
				if kind == "first-intent" && phase == publication.OldAsideIntent || kind == "second-intent" && phase == publication.CandidateCurrentIntent || kind == "visible-current" && phase == publication.CurrentObserved || kind == "final-record" && phase == publication.PublicationCommitted {
					return errors.New("fixture record refused")
				}
				if phase == publication.OldAsideIntent {
					if kind == "late-control" {
						host.control.Owner = "foreign"
					}
					if kind == "cancel" {
						cancel()
					}
				}
				return nil
			}
			out, err := NewEngine(EngineOptions{}).publishVerified(ctx, req, host, capability)
			if err == nil || out.Committed {
				t.Fatalf("callback failure earned completion: %+v %v", out, err)
			}
			l := req.Journal.Layout
			if kind == "first-intent" || kind == "late-control" || kind == "cancel" {
				if out.Mutated {
					t.Fatal("failed intent/authority/context permitted first rename")
				}
				old, err := LoadManifest(l.Current.Path)
				if err != nil || old.Generation != "current-generation" {
					t.Fatal("old current changed before admitted intent")
				}
			} else if kind == "second-intent" {
				if !out.Mutated {
					t.Fatal("first rename lost retained accounting")
				}
				if _, err := os.Stat(l.Current.Path); !os.IsNotExist(err) {
					t.Fatal("gap silently restored or candidate published")
				}
				if _, err := LoadManifest(l.Aside.Path); err != nil {
					t.Fatal("old aside lost")
				}
				if _, err := LoadManifest(l.Candidate.Path); err != nil {
					t.Fatal("unpublished candidate lost")
				}
			} else {
				if !out.Mutated {
					t.Fatal("visible publication lost retained accounting")
				}
				m, err := LoadManifest(l.Current.Path)
				if err != nil || m.Generation != "candidate-generation" {
					t.Fatal("visible current rolled back/deleted")
				}
				if _, err := LoadManifest(l.Aside.Path); err != nil {
					t.Fatal("old aside cleaned up")
				}
			}
		})
	}
}

func TestSyntheticPublicationRefusesChangedTargetsAndReplay(t *testing.T) {
	for _, kind := range []string{"occupied-aside", "candidate-replaced", "manifest-fifo", "manifest-hardlink", "replay-intent", "wrong-manifest"} {
		t.Run(kind, func(t *testing.T) {
			req, host, capability := publicationFixture(t, true)
			l := req.Journal.Layout
			switch kind {
			case "occupied-aside":
				if err := os.Mkdir(l.Aside.Path, 0700); err != nil {
					t.Fatal(err)
				}
			case "candidate-replaced":
				if err := os.Rename(l.Candidate.Path, l.Candidate.Path+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(l.Candidate.Path, 0700); err != nil {
					t.Fatal(err)
				}
			case "manifest-fifo":
				if err := os.Remove(ManifestPath(l.Candidate.Path)); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(ManifestPath(l.Candidate.Path), 0600); err != nil {
					t.Fatal(err)
				}
			case "manifest-hardlink":
				if err := os.Link(ManifestPath(l.Candidate.Path), filepath.Join(l.Candidate.Path, "retained-manifest-alias")); err != nil {
					t.Fatal(err)
				}
			case "wrong-manifest":
				if err := SaveManifest(l.Candidate.Path, Manifest{Generation: "foreign"}); err != nil {
					t.Fatal(err)
				}
			case "replay-intent":
				req.Journal.Events = append(req.Journal.Events, publication.Event{Sequence: 4, Phase: publication.OldAsideIntent})
				capability.original = req.Journal.Clone()
			}
			out, err := NewEngine(EngineOptions{}).publishVerified(context.Background(), req, host, capability)
			if err == nil || out.Mutated || out.Committed || len(host.records) != 0 {
				t.Fatalf("changed/replayed input reached intent/mutation: %+v %v", out, err)
			}
			if _, err := LoadManifest(l.Current.Path); err != nil {
				t.Fatal("refusal altered old current", err)
			}
		})
	}
}

func TestSyntheticPublicationLateAuthorityAndPhysicalCallbacks(t *testing.T) {
	for _, stage := range []publication.Phase{publication.OldAsideIntent, publication.CandidateCurrentIntent, publication.CurrentObserved, publication.PublicationCommitted} {
		for _, adverse := range []string{"revoked", "cancelled", "control-rebound", "pin-rebound", "candidate-manifest-changed"} {
			t.Run(string(stage)+"/"+adverse, func(t *testing.T) {
				req, host, capability := publicationFixture(t, true)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var revoked bool
				host.validate = func(publication.Journal) error {
					if revoked {
						return errors.New("SYNTHETIC authority expired/revoked")
					}
					return nil
				}
				host.hook = func(j publication.Journal) error {
					if j.Events[len(j.Events)-1].Phase != stage {
						return nil
					}
					switch adverse {
					case "revoked":
						revoked = true
					case "cancelled":
						cancel()
					case "control-rebound":
						host.control.Provenance = "foreign"
					case "pin-rebound":
						if err := os.Rename(j.Use.PinPath, j.Use.PinPath+"-retained"); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(j.Use.PinPath, []byte("foreign"), 0600); err != nil {
							t.Fatal(err)
						}
					case "candidate-manifest-changed":
						path := j.Layout.Candidate.Path
						if stage == publication.CurrentObserved || stage == publication.PublicationCommitted {
							path = j.Layout.Current.Path
						}
						if err := SaveManifest(path, Manifest{Generation: "foreign"}); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				}
				out, err := NewEngine(EngineOptions{}).publishVerified(ctx, req, host, capability)
				if err == nil || out.Committed || len(out.Retained) != 3 {
					t.Fatalf("late callback minted completion/lost retention: %+v %v", out, err)
				}
				if stage == publication.OldAsideIntent {
					if out.Mutated {
						t.Fatal("late first-intent callback permitted a rename")
					}
					if _, err := LoadManifest(req.Journal.Layout.Current.Path); err != nil {
						t.Fatal("current lost before first mutation", err)
					}
				} else {
					if !out.Mutated {
						t.Fatal("late refusal erased prior mutation")
					}
					if _, err := LoadManifest(req.Journal.Layout.Aside.Path); err != nil {
						t.Fatal("late failure removed prior old-current obligation", err)
					}
				}
			})
		}
	}
}

func TestSyntheticPublicationOpaqueAdmissionCannotRebindOriginalInputs(t *testing.T) {
	for _, field := range []string{"operation", "digest", "journal", "control", "aside", "candidate-generation", "reservation"} {
		t.Run(field, func(t *testing.T) {
			req, host, capability := publicationFixture(t, true)
			switch field {
			case "operation":
				capability.original.Origin.OperationID = "foreign"
			case "digest":
				capability.original.Origin.InputDigest = req.Journal.Layout.CandidateManifestDigest
			case "journal":
				capability.original.JournalID = "foreign"
			case "control":
				capability.original.Control.Owner = "foreign"
			case "aside":
				capability.original.Layout.Aside.Path += "-foreign"
			case "candidate-generation":
				capability.original.Layout.CandidateGeneration = "foreign"
			case "reservation":
				capability.original.Use.ReservationID = "foreign"
			}
			out, err := NewEngine(EngineOptions{}).publishVerified(context.Background(), req, host, capability)
			if err == nil || out.Mutated || out.Committed || len(host.records) != 0 {
				t.Fatal("opaque admission was transplanted into another original operation")
			}
			if _, err := LoadManifest(req.Journal.Layout.Current.Path); err != nil {
				t.Fatal("foreign original admission changed current", err)
			}
		})
	}
}
