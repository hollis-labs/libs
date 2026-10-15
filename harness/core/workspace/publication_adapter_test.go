package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
func (f *publicationAdapterPorts) ControlRoot() RootRef { _ = f.event("control"); return f.control }
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

func TestPublicationIntentCannotRestorePriorArtifactProofByRewritingPublicEvidence(t *testing.T) {
	p, f, result, j, held := publicationAdapterFixture(t)
	original, err := json.Marshal(result.Clone())
	if err != nil {
		t.Fatal(err)
	}
	host, err := newPublicationReceiptHost(p, adapterPorts(f), result, j, held)
	if err != nil {
		t.Fatal(err)
	}
	j.Events = append(j.Events, publication.Event{Sequence: 4, Phase: publication.CandidateCurrentIntent})
	if err := host.RecordPublication(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if f.records[0].Phase != Interrupted {
		t.Fatal("publication intent falsely recorded artifact-only completion phase")
	}
	if err := host.RecordPublication(context.Background(), j); err == nil {
		t.Fatal("same sequence replayed into sole receipt store")
	}
	// Restore all public fields exactly, leaving the private earned-proof state
	// alone. An attempted publication must monotonically invalidate that proof.
	// An omitted JSON field does not clear an existing pointer on Unmarshal;
	// explicitly clear that caller-writable field before restoring the snapshot.
	result.Receipt.PublicationJournal = nil
	if err := json.Unmarshal(original, result); err != nil {
		t.Fatal(err)
	}
	if result.ArtifactsComplete() || result.LaunchReady() {
		t.Fatal("caller restored a proof invalidated by publication intent")
	}
}

func TestPublicationCallbacksPreserveOriginalAccounting(t *testing.T) {
	for _, stage := range []string{"validate", "observe", "clock", "record"} {
		for _, variant := range []string{"receipt-header", "prior-obligation"} {
			t.Run(stage+"/"+variant, func(t *testing.T) {
				p, f, result, j, held := publicationAdapterFixture(t)
				original := result.Obligations[0]
				host, err := newPublicationReceiptHost(p, adapterPorts(f), result, j, held)
				if err != nil {
					t.Fatal(err)
				}
				j.Events = append(j.Events, publication.Event{Sequence: 4, Phase: publication.CandidateCurrentIntent})
				f.hook = func(kind string) error {
					if kind == stage {
						if variant == "receipt-header" {
							result.Receipt.OperationID = "foreign-callback-operation"
						} else {
							result.Obligations = nil
							result.Receipt.Obligations = nil
						}
					}
					return nil
				}
				err = host.RecordPublication(context.Background(), j)
				for _, record := range f.records {
					if record.OperationID != p.OperationID() || record.PublicationJournal == nil || record.PublicationJournal.Origin.OperationID != record.OperationID {
						t.Fatalf("foreign envelope persisted: header=%s journal=%s err=%v", record.OperationID, record.PublicationJournal.Origin.OperationID, err)
					}
					if !slices.Contains(record.Obligations, original) {
						t.Fatalf("original obligation erased before durable intent: err=%v receipt=%+v", err, record)
					}
				}
				if err == nil && variant == "prior-obligation" && !slices.Contains(result.Obligations, original) {
					t.Fatal("callback erased retained original obligation")
				}
				if err == nil && result.Receipt.OperationID != p.OperationID() {
					t.Fatal("callback left successful accounting bound to foreign operation")
				}
			})
		}
	}
}

// SYNTHETIC ledger admission; callbacks possess the public result, never owned
// accounting. Populate older envelopes so deep aliases cannot erase recovery.
func TestPublicationOwnsEntireAccountingAcrossEveryCallbackAndReturn(t *testing.T) {
	for _, stage := range []string{"validate", "observe", "clock", "control", "record"} {
		for _, mode := range []string{"replace", "nested", "failed-record"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				p, f, result, j, held := publicationAdapterFixture(t)
				prior := j.Clone()
				prior.Origin.OperationID = "earlier-original"
				prior.Origin.InputDigest = strings.Repeat("c", 64)
				pin, e := p.PinCreationIntent()
				if e != nil {
					t.Fatal(e)
				}
				pin.Origin = prior.Origin
				result.Receipt.PublicationOrigins = []PublicationOrigin{{SchemaVersion: SchemaVersion, OperationID: prior.Origin.OperationID, InputDigest: prior.Origin.InputDigest, IdentityKey: prior.Origin.IdentityKey, Journal: prior}}
				result.Receipt.PinOrigins = []PinCreationOrigin{{SchemaVersion: SchemaVersion, OperationID: pin.Origin.OperationID, InputDigest: pin.Origin.InputDigest, IdentityKey: pin.Origin.IdentityKey, Evidence: pin}}
				result.Retained = []RootRef{p.spec.Boot.Current}
				result.artifactSeal, result.artifactsComplete = resultSeal(*result)
				baseline := result.Clone()
				host, e := newPublicationReceiptHost(p, adapterPorts(f), result, j, held)
				if e != nil {
					t.Fatal(e)
				}
				f.hook = func(kind string) error {
					if kind == stage {
						if mode == "nested" {
							result.Receipt.Roots[0].Root.Owner = "foreign"
							result.Receipt.PublicationOrigins[0].Journal.Layout.Candidate.Owner = "foreign"
							result.Receipt.PinOrigins[0].Evidence.Grant.AuthorizationID = "foreign"
							result.Obligations[0].Code = "foreign"
							result.Retained[0].Owner = "foreign"
						} else {
							*result = ApplyResult{Status: Ready, Receipt: Receipt{OperationID: "foreign", Phase: ArtifactsCommitted}}
						}
						if mode == "failed-record" && kind == "record" {
							return errors.New("record failed after caller mutation")
						}
					}
					return nil
				}
				// Direct Validate must restore owned accounting even before any Record.
				if stage != "record" {
					if e := host.ValidatePublication(context.Background(), materialize.PublishRequest{Journal: j}); e != nil {
						t.Fatal(e)
					}
					if !reflect.DeepEqual(result.Clone(), baseline) {
						t.Fatal("validation returned caller-mutated accounting")
					}
				}
				j.Events = append(j.Events, publication.Event{Sequence: 4, Phase: publication.CandidateCurrentIntent})
				e = host.RecordPublication(context.Background(), j)
				if (mode == "failed-record" && stage == "record") != (e != nil) {
					t.Fatalf("record result %v", e)
				}
				for _, r := range f.records {
					if r.OperationID != baseline.Receipt.OperationID || r.InputDigest != baseline.Receipt.InputDigest || r.IdentityKey != baseline.Receipt.IdentityKey || !reflect.DeepEqual(r.Roots, baseline.Receipt.Roots) || !reflect.DeepEqual(r.Effects, baseline.Receipt.Effects) || !reflect.DeepEqual(r.RepositoryRequests, baseline.Receipt.RepositoryRequests) || !reflect.DeepEqual(r.RepositoryOrigins, baseline.Receipt.RepositoryOrigins) || !reflect.DeepEqual(r.PublicationOrigins, baseline.Receipt.PublicationOrigins) || !reflect.DeepEqual(r.PinOrigins, baseline.Receipt.PinOrigins) || !slices.Contains(r.Obligations, baseline.Obligations[0]) {
						t.Fatal("sole Record lost or rebound admitted accounting")
					}
				}
				if result.Receipt.OperationID != baseline.Receipt.OperationID || !reflect.DeepEqual(result.Receipt.PinOrigins, baseline.Receipt.PinOrigins) || !reflect.DeepEqual(result.Receipt.PublicationOrigins, baseline.Receipt.PublicationOrigins) || !slices.Contains(result.Obligations, baseline.Obligations[0]) || result.ArtifactsComplete() || result.LaunchReady() || result.Status != Partial || result.Receipt.Phase != Interrupted || len(result.Retained) != 3 {
					t.Fatal("return lost admitted accounting or monotone Partial")
				}
			})
		}
	}
}

func TestPublicationConstructorControlCallbackCannotRewriteAdmittedLedger(t *testing.T) {
	for _, kind := range []string{"replace", "nested", "journal"} {
		for _, refuse := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/success", true: "/refused"}[refuse], func(t *testing.T) {
				p, f, result, j, held := publicationAdapterFixture(t)
				original := result.Clone()
				originalJournal := j.Clone()
				f.hook = func(stage string) error {
					if stage == "control" {
						switch kind {
						case "replace":
							*result = ApplyResult{Status: Ready}
						case "nested":
							result.Receipt.Roots[0].Root.Owner = "foreign"
							result.Obligations[0].Code = "foreign"
						case "journal":
							j.Events[0].Phase = "foreign"
						}
						if refuse {
							f.control.Owner = "foreign"
						}
					}
					return nil
				}
				host, err := newPublicationReceiptHost(p, adapterPorts(f), result, j, held)
				if refuse != (err != nil) || !reflect.DeepEqual(result.Clone(), original) {
					t.Fatal("constructor callback replaced admitted result")
				}
				if !refuse && (!reflect.DeepEqual(host.accounting.Clone(), original) || !reflect.DeepEqual(host.last, originalJournal)) {
					t.Fatal("constructor captured mutable caller ledger or journal")
				}
			})
		}
	}
}

func TestPublicationCallbackCannotMutateAdmittedNextJournalEvents(t *testing.T) {
	for _, stage := range []string{"validate", "observe", "clock", "control", "record"} {
		t.Run(stage, func(t *testing.T) {
			p, f, result, j, held := publicationAdapterFixture(t)
			host, err := newPublicationReceiptHost(p, adapterPorts(f), result, j, held)
			if err != nil {
				t.Fatal(err)
			}
			j.Events = append(j.Events, publication.Event{Sequence: 4, Phase: publication.CandidateCurrentIntent})
			expected := j.Clone()
			f.hook = func(kind string) error {
				if kind == stage {
					j.Events[3].Phase = "foreign"
				}
				return nil
			}
			if err := host.RecordPublication(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			if len(f.records) != 1 || f.records[0].PublicationJournal == nil || !reflect.DeepEqual(*f.records[0].PublicationJournal, expected) || !reflect.DeepEqual(host.last, expected) || !reflect.DeepEqual(*result.Receipt.PublicationJournal, expected) {
				t.Fatal("callback event alias entered sole durable accounting")
			}
		})
	}
}
