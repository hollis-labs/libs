//go:build linux || darwin

package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
