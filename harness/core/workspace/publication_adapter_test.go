package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/publication"
)

type publicationAdapterPorts struct {
	control  RootRef
	observed Observations
	records  []Receipt
	hook     func(string) error
}

func (f *publicationAdapterPorts) event(kind string) error {
	if f.hook != nil {
		return f.hook(kind)
	}
	return nil
}
func (f *publicationAdapterPorts) Now() time.Time {
	_ = f.event("clock")
	return f.observed.At.Add(time.Second)
}
func (f *publicationAdapterPorts) Validate(context.Context, Spec, Resources) error {
	return f.event("validate")
}
func (f *publicationAdapterPorts) EnsureOwnedDirectory(context.Context, RootRef, fs.FileMode) error {
	panic("publication fixture must never write directories")
}
func (f *publicationAdapterPorts) Observe(context.Context, Resources) (Observations, error) {
	return copyRecord(f.observed), f.event("observe")
}
func (f *publicationAdapterPorts) ControlRoot() RootRef { return f.control }
func (f *publicationAdapterPorts) Record(_ context.Context, r Receipt) error {
	f.records = append(f.records, copyRecord(r))
	return f.event("record")
}
func (f *publicationAdapterPorts) Acquire(context.Context, LockKey) (HeldLock, error) {
	panic("unavailable root admission must never acquire")
}

type publicationAdapterHeld struct{}

func (publicationAdapterHeld) Release() error { return nil }

func publicationAdapterFixture(t *testing.T) (PlannedWorkspace, *publicationAdapterPorts, *ApplyResult, publication.Journal, []HeldLock) {
	t.Helper()
	s, c, r, o := publicationInputs(t)
	p := fixturePlanned(t, s, c, r, o)
	ref := func(root RootRef, inode uint64) publication.Root {
		out := publication.Root{ID: root.ID, Path: root.Path, Owner: root.Owner, Provenance: root.Provenance}
		if inode != 0 {
			out.Identity = publication.FileIdentity{Volume: "SYNTHETIC-fixture-only", Device: 1, Inode: inode}
		}
		return out
	}
	intent, err := p.PinCreationIntent()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(intent.Key.CanonicalID))
	parent, candidate := ref(s.Boot.IdentityRoot, 1), ref(s.Boot.Candidate, 2)
	j := publication.Journal{Version: publication.Version, JournalID: s.Publication.JournalID, Control: ref(r.LockRoot, 3), Origin: intent.Origin, Layout: publication.Layout{Parent: parent, Current: ref(s.Boot.Current, 0), Candidate: candidate, Aside: ref(s.Publication.Aside, 0), CandidateGeneration: p.Digest(), CandidateManifestDigest: s.Identity.ArtifactDigest.Hex}, Use: publication.UseBinding{Namespace: r.LockNamespace, CanonicalID: s.Boot.IdentityRoot.Path, MutationPath: filepath.Join(r.LockNamespace, "lock-"+hex.EncodeToString(hash[:])), PinPath: intent.Path, MutationIdentity: publication.FileIdentity{Volume: "SYNTHETIC-fixture-only", Device: 1, Inode: 4}, PinIdentity: publication.FileIdentity{Volume: "SYNTHETIC-fixture-only", Device: 1, Inode: 5}, ReservationID: s.Publication.ReservationID}, Events: []publication.Event{{Sequence: 1, Phase: publication.Planned}, {Sequence: 2, Phase: publication.CandidateVerified, ObservedIdentity: candidate.Identity, ManifestDigest: s.Identity.ArtifactDigest.Hex}, {Sequence: 3, Phase: publication.ReservationHeld}}}
	if err := j.Validate(); err != nil {
		t.Fatal(err)
	}
	f := &publicationAdapterPorts{control: r.LockRoot, observed: o}
	obligation := Obligation{Kind: RecoveryInspectionRequired, RootID: s.Boot.IdentityRoot.ID, Code: "earlier-uncertain-effect"}
	result := &ApplyResult{Status: Partial, Receipt: Receipt{SchemaVersion: SchemaVersion, OperationID: s.OperationID, InputDigest: p.Digest(), IdentityKey: s.Identity.EncodedKey, Identity: s.Identity, Phase: ArtifactsCommitted, Roots: []RootReceipt{{Root: s.Boot.Candidate, Generation: p.Digest(), Complete: true}}, Obligations: []Obligation{obligation}}, Obligations: []Obligation{obligation}}
	// In-package SYNTHETIC downstream seal. This does NOT run actual native
	// publication, earn metadata support, real use admission or launch readiness.
	result.artifactSeal, result.artifactsComplete = resultSeal(*result)
	held := []HeldLock{}
	for range p.LockKeys() {
		held = append(held, publicationAdapterHeld{})
	}
	return p, f, result, j, held
}

func adapterPorts(f *publicationAdapterPorts) Ports {
	return Ports{Clock: f, Host: f, Observations: f, ReceiptStore: f, Locks: f}
}

func TestActualRootPublicationUnavailableCallsNoPortDespiteValidFixturePorts(t *testing.T) {
	p, f, _, _, _ := publicationAdapterFixture(t)
	f.hook = func(string) error { panic("actual unavailable publication invoked port") }
	result, err := Materialize(context.Background(), p, adapterPorts(f))
	if err == nil || result.Status != Unsupported || result.ArtifactsComplete() || result.LaunchReady() || len(f.records) != 0 {
		t.Fatal("unknown actual capability invoked callbacks/receipts")
	}
}

func TestSyntheticPublicationAdapterUsesSoleStoreAndClosesLateCallbackWindows(t *testing.T) {
	for _, kind := range []string{"positive", "failed-record", "record-control", "clock-control", "observe-control", "validate-control", "cancelled", "foreign-operation", "foreign-digest"} {
		t.Run(kind, func(t *testing.T) {
			p, f, result, j, held := publicationAdapterFixture(t)
			host, err := newPublicationReceiptHost(p, adapterPorts(f), result, j, held)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			j.Events = append(j.Events, publication.Event{Sequence: 4, Phase: publication.CandidateCurrentIntent})
			if kind == "foreign-operation" {
				j.Origin.OperationID = "foreign"
			}
			if kind == "foreign-digest" {
				j.Origin.InputDigest = p.spec.Identity.SemanticDigest.Hex
			}
			f.hook = func(stage string) error {
				if kind == "failed-record" && stage == "record" {
					return errors.New("store Record failed")
				}
				if kind == stage+"-control" {
					f.control.Owner = "foreign"
				}
				if kind == "cancelled" && stage == "record" {
					cancel()
				}
				return nil
			}
			err = host.RecordPublication(ctx, j)
			if kind == "positive" {
				if err != nil || len(f.records) != 1 || f.records[0].PublicationJournal == nil || f.records[0].PublicationJournal.Origin != j.Origin || !slices.Contains(result.Obligations, Obligation{Kind: RecoveryInspectionRequired, RootID: p.spec.Boot.IdentityRoot.ID, Code: "earlier-uncertain-effect"}) {
					t.Fatalf("sole store/original accounting: %+v %v", result, err)
				}
				j.Events[3].Phase = publication.PublicationCommitted
				if host.last.Events[3].Phase != publication.CandidateCurrentIntent || result.Receipt.PublicationJournal.Events[3].Phase != publication.CandidateCurrentIntent {
					t.Fatal("caller altered frozen journal")
				}
			} else if err == nil {
				t.Fatal("late/foreign publication callback accepted")
			}
			if kind == "positive" || kind == "failed-record" || kind == "record-control" || kind == "cancelled" {
				if result.ArtifactsComplete() || result.LaunchReady() || len(result.Retained) != 3 {
					t.Fatal("intent failure lost retention or sealed completion")
				}
			}
			if kind == "clock-control" || kind == "observe-control" || kind == "validate-control" || kind == "foreign-operation" || kind == "foreign-digest" {
				if len(f.records) != 0 {
					t.Fatal("late admission or foreign origin permitted Record")
				}
			}
		})
	}
}

func TestPublicationJournalOriginalHeaderSurvivesRetryAndRejectsSplice(t *testing.T) {
	for _, kind := range []string{"positive", "own-operation", "own-digest", "inherited-operation", "inherited-digest"} {
		t.Run(kind, func(t *testing.T) {
			p, _, result, j, _ := publicationAdapterFixture(t)
			j.Events = append(j.Events, publication.Event{Sequence: 4, Phase: publication.CandidateCurrentIntent})
			result.Receipt.PublicationJournal = &j
			result.Receipt.Phase = Interrupted
			s, c, r, o := publicationInputs(t)
			if kind == "inherited-operation" || kind == "inherited-digest" {
				s.OperationID = "middle"
				r.RecoveryReceipts = []Receipt{result.Receipt}
				middle := fixturePlanned(t, s, c, r, o)
				got, _ := Materialize(context.Background(), middle, Ports{})
				result = &got
				j = result.Receipt.PublicationOrigins[0].Journal
			}
			if kind == "own-operation" || kind == "inherited-operation" {
				j.Origin.OperationID = "foreign"
			}
			if kind == "own-digest" || kind == "inherited-digest" {
				j.Origin.InputDigest = s.Identity.SemanticDigest.Hex
			}
			if len(result.Receipt.PublicationOrigins) > 0 {
				result.Receipt.PublicationOrigins[0].Journal = j
			} else {
				result.Receipt.PublicationJournal = &j
			}
			s.OperationID = "later"
			r.RecoveryReceipts = []Receipt{result.Receipt}
			later := fixturePlanned(t, s, c, r, o)
			got, err := Materialize(context.Background(), later, Ports{})
			if kind == "positive" {
				if err == nil || got.Status != Partial || len(got.Receipt.PublicationOrigins) != 1 || got.Receipt.PublicationOrigins[0].OperationID != p.OperationID() || got.Receipt.PublicationOrigins[0].InputDigest != p.Digest() {
					t.Fatalf("original journal lost/rebound: %+v %v", got, err)
				}
			} else if err == nil || got.Status != Conflict {
				t.Fatal("coherent inner journal splice admitted")
			}
		})
	}
}
