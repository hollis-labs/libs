//go:build linux || darwin

package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

func existingPinFixture(t *testing.T) (*ports, workspace.LockKey, *heldUnion, pinInode, string) {
	t.Helper()
	p, key, _ := custodyFixture(t)
	lock, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	union, err := p.bindOrderedHandles([]workspace.LockKey{key}, []workspace.HeldLock{lock})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(key.Namespace, "pin-"+digestName(key.CanonicalID))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("original-pin")); err != nil {
		t.Fatal(err)
	}
	inode, err := openedPinInode(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	return p, key, union, inode, path
}

func newPinFixture(t *testing.T) (*ports, workspace.PlannedWorkspace, []workspace.HeldLock, workspace.Receipt) {
	t.Helper()
	p, key, _ := custodyFixture(t)
	base := filepath.Dir(key.Namespace)
	urn := "urn:fixture:new-pin"
	encoded, err := bootkey.Encode(urn)
	if err != nil {
		t.Fatal(err)
	}
	ref := func(id, path string) workspace.RootRef {
		return workspace.RootRef{ID: id, Path: path, AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
	}
	parent := ref("identity", filepath.Join(base, encoded))
	if err := os.Mkdir(parent.Path, 0700); err != nil {
		t.Fatal(err)
	}
	home, current, candidate, aside := ref("home", filepath.Join(base, "home")), ref("current", filepath.Join(parent.Path, "current")), ref("candidate", filepath.Join(parent.Path, "candidate")), ref("aside", filepath.Join(parent.Path, "aside-original"))
	control := ref("control", key.Namespace)
	d := artifact.DigestBytes([]byte("SYNTHETIC native transport only"))
	s := workspace.Spec{SchemaVersion: workspace.SchemaVersion, OperationID: "original-new-pin", Operation: workspace.Prepare,
		Identity: workspace.IdentitySpec{AgentURN: urn, EncodedKey: encoded, Session: "fixture-session", DefinitionRevision: "fixture-revision", SemanticDigest: d, ArtifactDigest: d, DependencyDigest: d, Fence: workspace.ResourceRef{ID: "fixture-fence", Revision: "1"}},
		Home:     workspace.HomeSpec{Root: home, Layout: workspace.FullHome, Continuity: workspace.Durable, Retention: workspace.Keep},
		Boot:     workspace.BootSpec{IdentityRoot: parent, Current: current, Candidate: candidate, Retention: workspace.RetainForRecovery},
		CWD:      workspace.CWDSpec{RootID: home.ID, Relative: "."}, Sandbox: workspace.SandboxSpec{Policy: sandbox.ResolvedAccessPolicy{Mode: sandbox.ConfinementDisabled}}, Cleanup: workspace.CleanupPolicy{Retention: workspace.Keep}}
	g := workspace.EffectGrant{Kind: workspace.PublicationEffect, RootID: parent.ID, AuthorizationID: "fixture-publish-authority", Version: "1"}
	pg := workspace.EffectGrant{Kind: workspace.PinCreationEffect, RootID: parent.ID, AuthorizationID: "fixture-pin-authority", Version: "1"}
	s.Publication = &workspace.PublicationSpec{Control: control, Aside: aside, JournalID: "fixture-journal", ReservationID: "fixture-reservation", Authorization: g, PinCreationAuthorization: pg}
	s.Effects = []workspace.EffectGrant{{Kind: workspace.DirectoryEffect, RootID: home.ID, AuthorizationID: "fixture-directory", Version: "1"}, {Kind: workspace.DirectoryEffect, RootID: parent.ID, AuthorizationID: "fixture-directory", Version: "1"}, g, pg}
	r := workspace.Resources{Roots: []workspace.RootRef{home, parent, current, candidate, aside}, LockRoot: control, LockNamespace: control.Path, Grants: s.Effects, Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks}}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := workspace.Observations{At: at, ExpiresAt: at.Add(time.Minute), FenceVersion: s.Identity.Fence.Revision, Capabilities: r.Capabilities}
	for _, root := range append(append([]workspace.RootRef(nil), r.Roots...), control) {
		o.Roots = append(o.Roots, workspace.RootObservation{RootID: root.ID, DeclaredPath: root.Path, CanonicalPath: root.Path, CanonicalBase: root.AllowedBase, Owner: root.Owner, Exists: root.ID == control.ID || root.ID == parent.ID, Directory: root.ID == control.ID || root.ID == parent.ID})
	}
	plan, err := workspace.Plan(s, workspace.ResolvedContent{}, r, o)
	if err != nil {
		t.Fatal(err)
	}
	p.options.OperationID, p.options.ControlRoot, p.options.Resources = s.OperationID, control, r
	p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error { return nil }
	held := []workspace.HeldLock{}
	for _, k := range plan.LockKeys() {
		l, err := p.Acquire(context.Background(), k)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, l)
	}
	t.Cleanup(func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = held[i].Release()
		}
	})
	// Synthetic transport accounting, NOT a real artifact seal/metadata/absence
	// producer or an actually supported public publication operation.
	receipt := workspace.Receipt{SchemaVersion: workspace.SchemaVersion, OperationID: s.OperationID, InputDigest: plan.Digest(), IdentityKey: encoded, Identity: s.Identity, Phase: workspace.ArtifactsCommitted, Obligations: []workspace.Obligation{{Kind: workspace.RecoveryInspectionRequired, RootID: parent.ID, Code: "prior-uncertain-obligation"}}}
	if err := p.Record(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	return p, plan, held, receipt
}

func TestNewPinRequiresFullUnionAndDurableIntentThenPreservesInode(t *testing.T) {
	p, plan, held, receipt := newPinFixture(t)
	if _, err := p.preparePinCreation(context.Background(), plan, held[:len(held)-1], receipt); err == nil {
		t.Fatal("missing EX resource admitted creation")
	}
	intent, err := p.preparePinCreation(context.Background(), plan, held, receipt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.createNewPin(context.Background(), intent)
	if err != nil || !out.evidence.Created || !out.evidence.Uncertain || out.reservation == nil || out.evidence.Identity.Inode == 0 {
		t.Fatalf("new inode protocol: %+v %v", out, err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(out.reservation.file.Fd()), syscall.LOCK_UN)
		_ = out.reservation.file.Close()
	})
	if err := p.verifyPinReceipt(out.receipt); err != nil {
		t.Fatal("observed durable receipt differs", err)
	}
	if len(out.receipt.Obligations) != len(receipt.Obligations) || out.receipt.Obligations[0] != receipt.Obligations[0] {
		t.Fatal("new pin erased earlier uncertainty")
	}
	if _, err := p.createNewPin(context.Background(), intent); err == nil {
		t.Fatal("same token replayed after observed receipt")
	}
	for _, l := range held {
		if err := l.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.reservation.validatePinCustody(context.Background()); err != nil {
		t.Fatal("caller SH disappeared on EX release", err)
	}
	other, err := os.OpenFile(out.evidence.Path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("supplemental SH did not exclude EX")
	}
}

func TestNewPinRejectsIntentFailuresAndLateAuthorityWithoutCleanup(t *testing.T) {
	for _, kind := range []string{"foreign-receipt", "failed-record", "post-record-cancel", "late-authority", "receipt-rebound", "occupied-after-intent", "observed-authority", "late-configured-control", "late-configured-grant"} {
		t.Run(kind, func(t *testing.T) {
			p, plan, held, receipt := newPinFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e, _ := plan.PinCreationIntent()
			if kind == "foreign-receipt" {
				receipt.InputDigest = "foreign"
			}
			if kind == "failed-record" {
				path := filepath.Join(p.options.ControlRoot.Path, "receipt-lock-"+digestName(p.options.OperationID))
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "post-record-cancel" {
				p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
					r, err := p.readReceipt()
					if err != nil {
						t.Fatal(err)
					}
					if r.PinCreation != nil {
						cancel()
					}
					return nil
				}
			}
			intent, err := p.preparePinCreation(ctx, plan, held, receipt)
			if kind == "foreign-receipt" || kind == "failed-record" || kind == "post-record-cancel" {
				if err == nil || intent != nil {
					t.Fatal("failed original intent earned creation token")
				}
				if _, err := os.Lstat(e.Path); !os.IsNotExist(err) {
					t.Fatal("failed original intent created pin")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "late-configured-control":
				p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
					p.options.ControlRoot.Owner = "foreign"
					return nil
				}
			case "late-configured-grant":
				p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
					p.options.Resources.Grants = nil
					return nil
				}
			case "late-authority":
				p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
					return errors.New("expired frozen grant")
				}
			case "receipt-rebound":
				p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
					bad := intent.receipt
					bad.OperationID = "foreign"
					data, _ := json.Marshal(bad)
					if err := p.control.WriteFile("receipt-"+digestName(p.options.OperationID)+".json", data, 0600); err != nil {
						t.Fatal(err)
					}
					return nil
				}
			case "occupied-after-intent":
				if err := os.WriteFile(e.Path, []byte("retained-existing-inode"), 0600); err != nil {
					t.Fatal(err)
				}
			case "observed-authority":
				p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
					r, err := p.readReceipt()
					if err != nil {
						t.Fatal(err)
					}
					if r.PinCreation != nil && r.PinCreation.Created {
						return errors.New("expired after observed Record")
					}
					return nil
				}
			}
			out, err := p.createNewPin(ctx, intent)
			if err == nil || out.reservation != nil {
				t.Fatal("late callback/occupation earned shared use admission")
			}
			if kind == "observed-authority" {
				if !out.evidence.Created || !out.evidence.Uncertain {
					t.Fatal("late failure erased actual-created uncertain evidence")
				}
				if _, err := os.Lstat(e.Path); err != nil {
					t.Fatal("late failure removed retained pin", err)
				}
			} else if kind == "occupied-after-intent" {
				b, err := os.ReadFile(e.Path)
				if err != nil || string(b) != "retained-existing-inode" || out.evidence.Created {
					t.Fatal("existing inode adopted/truncated")
				}
			} else if _, err := os.Lstat(e.Path); !os.IsNotExist(err) {
				t.Fatal("precreation refusal mutated pin")
			}
		})
	}
}

func TestExistingPinProbeThenSeparateSharedDescriptor(t *testing.T) {
	p, key, union, inode, path := existingPinFixture(t)
	reservation, err := p.reserveExistingPin(context.Background(), key, inode, union)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit fixture teardown: no child/start has occurred. This is not a
	// production unconditional-release or abandonment policy.
	t.Cleanup(func() { _ = syscall.Flock(int(reservation.file.Fd()), syscall.LOCK_UN); _ = reservation.file.Close() })
	if reservation.inode != inode || reservation.owner != p || reservation.union != union || reservation.name != filepath.Base(path) {
		t.Fatal("shared reservation lost original custody")
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, reservation.file.Fd(), syscall.F_GETFD, 0)
	if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("shared descriptor inheritable")
	}
	other, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatalf("temporary EX descriptor survived probe, separate SH cannot overlap: %v", err)
	}
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	err = syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("SH does not exclude EX: %v", err)
	}
	if err := union.validate(); err != nil {
		t.Fatal("mutation union released by pin protocol", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "original-pin" {
		t.Fatal("pin contents replaced or truncated")
	}
}

func TestCallerSharedPinSurvivesMutationReleaseAndRefusesLateReplacement(t *testing.T) {
	p, key, union, inode, path := existingPinFixture(t)
	pin, err := p.reserveExistingPin(context.Background(), key, inode, union)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Flock(int(pin.file.Fd()), syscall.LOCK_UN); _ = pin.file.Close() })
	if err := union.locks[0].Release(); err != nil {
		t.Fatal(err)
	}
	if err := pin.validatePinCustody(context.Background()); err != nil {
		t.Fatal("caller SH lost when mutEX released", err)
	}
	probe, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatal("caller shared pin released unconditionally", err)
	}
	if err := os.Rename(path, path+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := pin.validatePinCustody(context.Background()); err == nil {
		t.Fatal("late different named pin inode accepted")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "replacement" {
		t.Fatal("late mismatch repaired foreign pin")
	}
}

func TestExistingPinRefusesUnknownChangedUsedAndReleasedCustody(t *testing.T) {
	for _, kind := range []string{"unknown-inode", "foreign-inode", "absent", "hard-link", "in-use", "released-union", "cancelled", "foreign-ports"} {
		t.Run(kind, func(t *testing.T) {
			p, key, union, inode, path := existingPinFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "unknown-inode":
				inode = pinInode{}
			case "foreign-inode":
				inode.inode++
			case "absent":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "hard-link":
				if err := os.Link(path, path+"-alias"); err != nil {
					t.Fatal(err)
				}
			case "in-use":
				file, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() })
				if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			case "released-union":
				if err := union.locks[0].Release(); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			case "foreign-ports":
				other := *p
				p = &other
			}
			reserved, err := p.reserveExistingPin(ctx, key, inode, union)
			if reserved != nil {
				_ = syscall.Flock(int(reserved.file.Fd()), syscall.LOCK_UN)
				_ = reserved.file.Close()
			}
			if err == nil || reserved != nil {
				t.Fatal("unknown/changed/cooperatively used pin admitted")
			}
			if kind == "absent" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("absent pin created without grant/intent")
				}
			}
		})
	}
}

func TestHeldMutationUnionRejectsOmittedReorderedAndForeignHandles(t *testing.T) {
	p, key, _ := custodyFixture(t)
	second := key
	second.CanonicalID += "-second"
	p.options.Resources.Roots = append(p.options.Resources.Roots, workspace.RootRef{Path: second.CanonicalID})
	first, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	last, err := p.Acquire(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	defer last.Release()
	keys := []workspace.LockKey{key, second}
	locks := []workspace.HeldLock{first, last}
	if _, err := p.bindOrderedHandles(keys, locks); err != nil {
		t.Fatal("complete ordered native union refused", err)
	}
	for _, kind := range []string{"omitted-handle", "swapped-handles", "duplicate-key", "reordered-key", "foreign-ports"} {
		t.Run(kind, func(t *testing.T) {
			k := append([]workspace.LockKey(nil), keys...)
			l := append([]workspace.HeldLock(nil), locks...)
			owner := p
			switch kind {
			case "omitted-handle":
				l = l[:1]
			case "swapped-handles":
				l[0], l[1] = l[1], l[0]
			case "duplicate-key":
				k[1] = k[0]
			case "reordered-key":
				k[0], k[1] = k[1], k[0]
			case "foreign-ports":
				clone := *p
				owner = &clone
			}
			if _, err := owner.bindOrderedHandles(k, l); err == nil {
				t.Fatal("incomplete/foreign union admitted")
			}
		})
	}
}

func TestNativeUnionRequiresAnImmutableRootPlan(t *testing.T) {
	p, key, _ := custodyFixture(t)
	lock, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := p.bindHeldUnion(workspace.PlannedWorkspace{}, []workspace.HeldLock{lock}); err == nil {
		t.Fatal("native descriptors alone inferred a complete frozen root mutation union")
	}
}

func TestNativeUnionBindsWholeFrozenPlanOperationAndResources(t *testing.T) {
	p, key, _ := custodyFixture(t)
	base := filepath.Dir(key.Namespace)
	encoded, err := bootkey.Encode("urn:fixture:native-plan")
	if err != nil {
		t.Fatal(err)
	}
	ref := func(id, path string) workspace.RootRef {
		return workspace.RootRef{ID: id, Path: path, AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
	}
	home := ref("home", filepath.Join(base, "home"))
	parent := ref("identity", filepath.Join(base, encoded))
	current := ref("current", filepath.Join(parent.Path, "current"))
	candidate := ref("candidate", filepath.Join(parent.Path, "candidate"))
	control := ref("control", key.Namespace)
	d := artifact.DigestBytes([]byte("fixture"))
	s := workspace.Spec{SchemaVersion: workspace.SchemaVersion, OperationID: "original-native-plan", Operation: workspace.Prepare,
		Identity: workspace.IdentitySpec{AgentURN: "urn:fixture:native-plan", EncodedKey: encoded, Session: "fixture-session", DefinitionRevision: "fixture-revision", SemanticDigest: d, ArtifactDigest: d, DependencyDigest: d, Fence: workspace.ResourceRef{ID: "fixture-fence", Revision: "1"}},
		Home:     workspace.HomeSpec{Root: home, Layout: workspace.FullHome, Continuity: workspace.Durable, Retention: workspace.Keep},
		Boot:     workspace.BootSpec{IdentityRoot: parent, Current: current, Candidate: candidate, Retention: workspace.RetainForRecovery},
		CWD:      workspace.CWDSpec{RootID: home.ID, Relative: "."}, Sandbox: workspace.SandboxSpec{Policy: sandbox.ResolvedAccessPolicy{Mode: sandbox.ConfinementDisabled}}, Cleanup: workspace.CleanupPolicy{Retention: workspace.Keep}}
	s.Effects = []workspace.EffectGrant{{Kind: workspace.DirectoryEffect, RootID: home.ID, AuthorizationID: "fixture-authority", Version: "1"}, {Kind: workspace.DirectoryEffect, RootID: parent.ID, AuthorizationID: "fixture-authority", Version: "1"}}
	r := workspace.Resources{Roots: []workspace.RootRef{home, parent, current, candidate}, LockRoot: control, LockNamespace: control.Path, Grants: s.Effects, Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks}}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := workspace.Observations{At: at, ExpiresAt: at.Add(time.Minute), FenceVersion: s.Identity.Fence.Revision, Capabilities: r.Capabilities}
	for _, root := range append(append([]workspace.RootRef(nil), r.Roots...), control) {
		o.Roots = append(o.Roots, workspace.RootObservation{RootID: root.ID, DeclaredPath: root.Path, CanonicalPath: root.Path, CanonicalBase: root.AllowedBase, Owner: root.Owner, Exists: root.ID == control.ID, Directory: root.ID == control.ID})
	}
	plan, err := workspace.Plan(s, workspace.ResolvedContent{}, r, o)
	if err != nil {
		t.Fatal(err)
	}
	p.options.OperationID = s.OperationID
	p.options.ControlRoot = control
	p.options.Resources = r
	locks := []workspace.HeldLock{}
	for _, key := range plan.LockKeys() {
		l, err := p.Acquire(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		locks = append(locks, l)
	}
	t.Cleanup(func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = locks[i].Release()
		}
	})
	if _, err := p.bindHeldUnion(plan, locks); err != nil {
		t.Fatal("actual immutable full root union refused", err)
	}
	if _, err := p.bindHeldUnion(plan, locks[:len(locks)-1]); err == nil {
		t.Fatal("one required frozen mutation resource omitted")
	}
	s.OperationID = "foreign-native-plan"
	foreign, err := workspace.Plan(s, workspace.ResolvedContent{}, r, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.bindHeldUnion(foreign, locks); err == nil {
		t.Fatal("foreign operation inferred native admission")
	}
	p.options.Resources.Grants = nil
	if _, err := p.bindHeldUnion(plan, locks); err == nil {
		t.Fatal("changed host resource authority admitted")
	}
}

func TestNewPinDurablyRetainsUncertaintyAfterObservedCustodyFailure(t *testing.T) {
	p, plan, held, receipt := newPinFixture(t)
	intent, err := p.preparePinCreation(context.Background(), plan, held, receipt)
	if err != nil {
		t.Fatal(err)
	}
	p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
		r, readErr := p.readReceipt()
		if readErr != nil {
			return readErr
		}
		if r.PinCreation != nil && r.PinCreation.Created {
			return errors.New("SYNTHETIC late authority failure after observed inode record")
		}
		return nil
	}
	out, err := p.createNewPin(context.Background(), intent)
	if err == nil || !out.evidence.Created || !out.evidence.Uncertain || out.reservation != nil {
		t.Fatalf("late failure did not return created uncertainty: out=%+v err=%v", out, err)
	}
	durable, readErr := p.readReceipt()
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("SYNTHETIC returned_created=%t returned_uncertain=%t durable_created=%t durable_uncertain=%t durable_inode=%d", out.evidence.Created, out.evidence.Uncertain, durable.PinCreation != nil && durable.PinCreation.Created, durable.PinCreation != nil && durable.PinCreation.Uncertain, func() uint64 {
		if durable.PinCreation == nil {
			return 0
		}
		return durable.PinCreation.Identity.Inode
	}())
	if durable.PinCreation == nil || !durable.PinCreation.Created || !durable.PinCreation.Uncertain {
		t.Fatal("created pin failure was not durably marked uncertain")
	}
}

// Actual owned temp inodes, SYNTHETIC authority/admission. Conservative first
// observation survives lost custody without a compensating write or recreation.
func TestNewPinFirstObservationRetainsUncertaintyWithoutLateDurableWrite(t *testing.T) {
	for _, kind := range []string{"authority", "control-owner", "control-replaced", "grant", "cancel", "union-released", "pin-replaced", "in-use"} {
		t.Run(kind, func(t *testing.T) {
			p, plan, held, receipt := newPinFixture(t)
			intent, err := p.preparePinCreation(context.Background(), plan, held, receipt)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			name := "receipt-" + digestName(p.options.OperationID) + ".json"
			var recorded []byte
			var changed bool
			var other *os.File
			t.Cleanup(func() {
				if other != nil {
					_ = syscall.Flock(int(other.Fd()), syscall.LOCK_UN)
					_ = other.Close()
				}
			})
			p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
				r, err := p.readReceipt()
				if err != nil {
					return err
				}
				if r.PinCreation == nil || !r.PinCreation.Created || changed {
					return nil
				}
				changed = true
				recorded, err = p.control.ReadFile(name)
				if err != nil {
					return err
				}
				switch kind {
				case "authority":
					return errors.New("authority lost after first inode observation")
				case "control-owner":
					p.options.ControlRoot.Owner = "foreign"
				case "control-replaced":
					path := p.options.ControlRoot.Path
					if err := os.Rename(path, path+".retained"); err != nil {
						return err
					}
					if err := os.Mkdir(path, 0700); err != nil {
						return err
					}
				case "grant":
					p.options.Resources.Grants = nil
				case "cancel":
					cancel()
				case "union-released":
					if err := held[0].Release(); err != nil {
						return err
					}
				case "pin-replaced":
					path := r.PinCreation.Path
					if err := os.Rename(path, path+".retained"); err != nil {
						return err
					}
					if err := os.WriteFile(path, []byte("replacement preserved"), 0600); err != nil {
						return err
					}
				case "in-use":
					other, err = os.OpenFile(r.PinCreation.Path, os.O_RDWR, 0)
					if err != nil {
						return err
					}
					if err := syscall.Flock(int(other.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
						return err
					}
				}
				return nil
			}
			out, err := p.createNewPin(ctx, intent)
			if err == nil || !changed || out.reservation != nil || !out.evidence.Created || !out.evidence.Uncertain || out.evidence.Identity.Inode == 0 {
				t.Fatalf("late failure lost original inode/uncertainty: %+v %v", out, err)
			}
			after, readErr := p.control.ReadFile(name)
			if readErr != nil || !bytes.Equal(after, recorded) {
				t.Fatal("attempted compensating durable write after custody loss")
			}
			var durable workspace.Receipt
			if err := json.Unmarshal(after, &durable); err != nil {
				t.Fatal(err)
			}
			if durable.PinCreation == nil || !durable.PinCreation.Created || !durable.PinCreation.Uncertain || durable.PinCreation.Identity != out.evidence.Identity || durable.OperationID != receipt.OperationID || durable.InputDigest != receipt.InputDigest || !slices.Equal(durable.Obligations, receipt.Obligations) {
				t.Fatal("first durable observation lost uncertainty/original obligations")
			}
			path := out.evidence.Path
			if kind == "control-replaced" {
				path = filepath.Join(p.options.ControlRoot.Path+".retained", filepath.Base(path))
			}
			if kind == "pin-replaced" {
				path += ".retained"
				data, err := p.control.ReadFile(filepath.Base(out.evidence.Path))
				if err != nil || string(data) != "replacement preserved" {
					t.Fatal("replacement inode adopted or overwritten")
				}
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal("retained original inode disappeared", err)
			}
			stat := info.Sys().(*syscall.Stat_t)
			if uint64(stat.Dev) != out.evidence.Identity.Device || stat.Ino != out.evidence.Identity.Inode {
				t.Fatal("original inode was recreated")
			}
		})
	}
}

func TestNewPinLateCallbackCannotEraseOriginalObligationThroughInputAlias(t *testing.T) {
	p, plan, held, receipt := newPinFixture(t)
	original := receipt.Obligations[0]
	intent, err := p.preparePinCreation(context.Background(), plan, held, receipt)
	if err != nil {
		t.Fatal(err)
	}
	p.options.ValidateAuthority = func(context.Context, workspace.Spec, workspace.Resources) error {
		r, err := p.readReceipt()
		if err != nil {
			return err
		}
		if r.PinCreation != nil && r.PinCreation.Created {
			receipt.Obligations[0].Code = "foreign-callback"
			return errors.New("lost authority after observed inode")
		}
		return nil
	}
	out, err := p.createNewPin(context.Background(), intent)
	if err == nil || out.reservation != nil || !slices.Contains(out.receipt.Obligations, original) {
		t.Fatal("late input alias erased original returned recovery obligation")
	}
	durable, err := p.readReceipt()
	if err != nil || !slices.Contains(durable.Obligations, original) || durable.PinCreation == nil || !durable.PinCreation.Uncertain {
		t.Fatal("late input alias erased original durable uncertainty")
	}
}

func TestNewPinReturnedEvidenceCannotRewriteOriginalDurableIntent(t *testing.T) {
	p, plan, held, receipt := newPinFixture(t)
	original := receipt.Obligations[0]
	intent, err := p.preparePinCreation(context.Background(), plan, held, receipt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.createNewPin(context.Background(), intent)
	if err != nil || out.reservation == nil {
		t.Fatal("synthetic positive transport", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(out.reservation.file.Fd()), syscall.LOCK_UN)
		_ = out.reservation.file.Close()
	})
	out.receipt.Obligations[0].Code = "caller-mutation"
	if !slices.Contains(intent.receipt.Obligations, original) {
		t.Fatal("returned evidence rewrote private original intent")
	}
	durable, err := p.readReceipt()
	if err != nil || !slices.Contains(durable.Obligations, original) || !durable.PinCreation.Uncertain {
		t.Fatal("caller mutation changed durable original uncertainty")
	}
}

func TestNewPinIntentOwnsAdmittedReceiptBeforeCallerMutation(t *testing.T) {
	p, plan, held, receipt := newPinFixture(t)
	original := receipt.Obligations[0]
	intent, err := p.preparePinCreation(context.Background(), plan, held, receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Obligations[0].Code = "caller-mutation-after-intent"
	if !slices.Contains(intent.receipt.Obligations, original) {
		t.Fatal("caller owns private successfully-recorded intent accounting")
	}
	out, err := p.createNewPin(context.Background(), intent)
	if err != nil || out.reservation == nil || !slices.Contains(out.receipt.Obligations, original) {
		t.Fatal("original intent rebound or refused after caller-only mutation", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(out.reservation.file.Fd()), syscall.LOCK_UN)
		_ = out.reservation.file.Close()
	})
}
