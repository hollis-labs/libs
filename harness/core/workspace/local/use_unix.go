//go:build linux || darwin

package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"syscall"

	"github.com/hollis-labs/substrate/harness/workspace"
)

// heldUnion is private local custody, never a caller-authored proof. The root
// integration must supply exactly the frozen plan's complete ordered union.
// It is not an absence, metadata capability, authority or readiness assertion.
type heldUnion struct {
	owner *ports
	keys  []workspace.LockKey
	locks []*heldLock
}

// pinCreationIntent is minted only by successful SAME-store durable Record
// under an admitted immutable full union. An intent-looking file or public
// evidence value cannot construct this private execution token.
type pinCreationIntent struct {
	owner   *ports
	plan    workspace.PlannedWorkspace
	union   *heldUnion
	receipt workspace.Receipt
}

// verifyPinReceipt checks the exact original accounting bytes after authority
// callbacks, with bounded NOFOLLOW/NONBLOCK descriptor custody. It does not
// infer that a visible receipt was ever successfully synchronized.
func (p *ports) verifyPinReceipt(expected workspace.Receipt) error {
	name := "receipt-" + digestName(p.options.OperationID) + ".json"
	info, err := p.control.Lstat(name)
	if err != nil || !privateNative(info, false) || info.Size() > 16<<20 {
		return errors.Join(errors.New("local: pin receipt custody unavailable"), err)
	}
	file, err := p.control.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = p.validateFileCustody(file, name); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	encoded, encodeErr := json.Marshal(expected)
	if err = errors.Join(err, encodeErr, p.validateFileCustody(file, name)); err != nil {
		return err
	}
	if len(data) > 16<<20 || !bytes.Equal(data, encoded) {
		return errors.New("local: original pin receipt changed")
	}
	return nil
}

func (p *ports) preparePinCreation(ctx context.Context, plan workspace.PlannedWorkspace, held []workspace.HeldLock, receipt workspace.Receipt) (*pinCreationIntent, error) {
	if ctx == nil {
		return nil, errors.New("local: pin context required")
	}
	union, err := p.bindHeldUnion(plan, held)
	if err != nil {
		return nil, err
	}
	evidence, err := plan.PinCreationIntent()
	if err != nil {
		return nil, err
	}
	if receipt.SchemaVersion != workspace.SchemaVersion || receipt.OperationID != evidence.Origin.OperationID || receipt.InputDigest != evidence.Origin.InputDigest || receipt.IdentityKey != evidence.Origin.IdentityKey || receipt.Identity != plan.Spec().Identity || receipt.Phase != workspace.ArtifactsCommitted || receipt.PinCreation != nil || evidence.Control != p.options.ControlRoot {
		return nil, errors.New("local: original operation accounting required")
	}
	if err = p.validatePinPlan(ctx, plan, evidence); err != nil {
		return nil, err
	}
	if err = errors.Join(ctx.Err(), union.validate(), p.verifyPinReceipt(receipt)); err != nil {
		return nil, err
	}
	name := "pin-" + digestName(evidence.Key.CanonicalID)
	if _, err = p.control.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("local: existing pin cannot be created or adopted")
	}
	receipt.PinCreation = &evidence
	receipt.Phase = workspace.Interrupted
	// Failed sync/Record returns no token even if its file is visible. Preserve
	// all supplied earlier obligations/origins; this is the one durable store.
	if err = p.Record(ctx, receipt); err != nil {
		return nil, err
	}
	intent := &pinCreationIntent{owner: p, plan: plan, union: union, receipt: receipt}
	if err = intent.validate(ctx); err != nil {
		return nil, err
	}
	return intent, nil
}

func (i *pinCreationIntent) validate(ctx context.Context) error {
	if i == nil || i.owner == nil || i.union == nil || i.receipt.PinCreation == nil || ctx == nil {
		return errors.New("local: durable pin intent required")
	}
	expected, err := i.plan.PinCreationIntent()
	if err != nil {
		return err
	}
	actual := *i.receipt.PinCreation
	actual.Identity, actual.Created, actual.Uncertain = workspace.NativePinIdentity{}, false, false
	if actual != expected || i.receipt.SchemaVersion != workspace.SchemaVersion || i.receipt.OperationID != expected.Origin.OperationID || i.receipt.InputDigest != expected.Origin.InputDigest || i.receipt.IdentityKey != expected.Origin.IdentityKey || i.receipt.Identity != i.plan.Spec().Identity || i.receipt.Phase != workspace.Interrupted {
		return errors.New("local: original pin intent binding changed")
	}
	if err := i.owner.validatePinPlan(ctx, i.plan, expected); err != nil {
		return err
	}
	// No callback follows these checks before the controlled filesystem effect.
	return errors.Join(ctx.Err(), i.union.validate(), i.owner.verifyPinReceipt(i.receipt))
}

func (p *ports) validatePinPlan(ctx context.Context, plan workspace.PlannedWorkspace, evidence workspace.PinCreationEvidence) error {
	if err := p.Validate(ctx, plan.Spec(), plan.Resources()); err != nil {
		return err
	}
	// Configured custody/authority may change INSIDE that final callback. Check
	// the actual selected control tuple and frozen host grants after it returns,
	// before Record, pin creation or shared admission. No subsequent callbacks.
	if p.options.OperationID != plan.OperationID() || p.options.ControlRoot != evidence.Control || !sameResources(p.options.Resources, plan.Resources()) {
		return errors.New("local: configured pin authority changed after callback")
	}
	return ctx.Err()
}

type createdPin struct {
	evidence    workspace.PinCreationEvidence
	receipt     workspace.Receipt
	reservation *usePin
}

// createNewPin is still private and unreachable from the public root while its
// aggregate native metadata preflight refuses. Native fixture execution tests
// only the enrolled grant/intent/inode protocol, not publication support.
func (p *ports) createNewPin(ctx context.Context, intent *pinCreationIntent) (out createdPin, err error) {
	if intent == nil || intent.owner != p || intent.receipt.PinCreation == nil {
		return out, errors.New("local: foreign pin intent")
	}
	out.evidence, out.receipt = *intent.receipt.PinCreation, intent.receipt
	out.receipt.PinCreation = &out.evidence
	if out.evidence.Created || out.evidence.Uncertain || out.evidence.Identity != (workspace.NativePinIdentity{}) {
		return out, errors.New("local: pin creation replay unsupported")
	}
	if err = intent.validate(ctx); err != nil {
		return out, err
	}
	name := "pin-" + digestName(out.evidence.Key.CanonicalID)
	if _, err = p.control.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return out, errors.New("local: existing pin cannot be adopted")
	}
	file, err := p.control.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		out.evidence.Uncertain = !errors.Is(err, os.ErrExist)
		return out, err
	}
	out.evidence.Created = true
	// Close an unreserved creation descriptor only. Successful SH reservations
	// have no unconditional production release; failures never unlink the inode.
	err = errors.Join(file.Sync(), p.validateFileCustody(file, name))
	inode, identityErr := openedPinInode(file)
	if identityErr == nil {
		out.evidence.Identity = workspace.NativePinIdentity{Device: inode.device, Inode: inode.inode}
	}
	err = errors.Join(err, identityErr, file.Close())
	dir, openErr := p.control.Open(".")
	if openErr == nil {
		err = errors.Join(err, dir.Sync(), dir.Close())
	} else {
		err = errors.Join(err, openErr)
	}
	if err != nil {
		out.evidence.Uncertain = true
		return out, err
	}
	// Observed inode accounting remains interrupted until the root has earned
	// later publication/handoff facts. No phase label confers Ready or absence.
	out.receipt.PinCreation = &out.evidence
	if err = p.Record(ctx, out.receipt); err != nil {
		out.evidence.Uncertain = true
		return out, err
	}
	observed := &pinCreationIntent{owner: p, plan: intent.plan, union: intent.union, receipt: out.receipt}
	if err = observed.validate(ctx); err != nil {
		out.evidence.Uncertain = true
		return out, err
	}
	out.reservation, err = p.reserveExistingPin(ctx, out.evidence.Key, inode, intent.union)
	if err != nil {
		out.evidence.Uncertain = true
	}
	return out, err
}

func (p *ports) bindHeldUnion(plan workspace.PlannedWorkspace, locks []workspace.HeldLock) (*heldUnion, error) {
	if !plan.Valid() || plan.Digest() == "" || plan.OperationID() != p.options.OperationID || !sameResources(plan.Resources(), p.options.Resources) {
		return nil, errors.New("local: frozen root plan required for complete mutation union")
	}
	return p.bindOrderedHandles(plan.LockKeys(), locks)
}

// This lower-level check establishes correspondence to the supplied frozen key
// set, not completeness by itself. Production admission uses bindHeldUnion's
// immutable root plan; native fixtures can exercise descriptor mechanics here.
func (p *ports) bindOrderedHandles(keys []workspace.LockKey, locks []workspace.HeldLock) (*heldUnion, error) {
	if len(keys) == 0 || len(keys) != len(locks) {
		return nil, errors.New("local: complete held mutation union required")
	}
	u := &heldUnion{owner: p, keys: slices.Clone(keys)}
	for i, key := range keys {
		if key.Namespace != p.options.ControlRoot.Path || i > 0 && (keys[i-1].Namespace > key.Namespace || keys[i-1].Namespace == key.Namespace && keys[i-1].CanonicalID >= key.CanonicalID) {
			return nil, errors.New("local: mutation union must be ordered and deduplicated")
		}
		allowed := slices.ContainsFunc(p.options.Resources.Roots, func(r workspace.RootRef) bool { return r.Path == key.CanonicalID })
		lock, ok := locks[i].(*heldLock)
		if !allowed || !ok || lock == nil || lock.owner != p || lock.name != "lock-"+digestName(key.CanonicalID) {
			return nil, errors.New("local: mutation union is not owned by these ports")
		}
		u.locks = append(u.locks, lock)
	}
	if err := u.validate(); err != nil {
		return nil, err
	}
	return u, nil
}

func (u *heldUnion) validate() error {
	if u == nil || u.owner == nil || len(u.locks) == 0 || len(u.locks) != len(u.keys) {
		return errors.New("local: mutation union unavailable")
	}
	for _, lock := range u.locks {
		if !lock.active.Load() {
			return errors.New("local: mutation union released")
		}
		if err := u.owner.validateFileCustody(lock.file, lock.name); err != nil {
			return err
		}
	}
	return nil
}

type pinInode struct{ device, inode uint64 }

func openedPinInode(file *os.File) (pinInode, error) {
	info, err := file.Stat()
	if err != nil {
		return pinInode{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return pinInode{}, errors.New("local: native pin identity unsupported")
	}
	return pinInode{uint64(stat.Dev), stat.Ino}, nil
}

// usePin deliberately has no unconditional Close. The root lifecycle owns
// release only after matched adoption or positive authorized abandonment.
// This private transport earns neither public launch admission nor Ready.
type usePin struct {
	owner *ports
	union *heldUnion
	file  *os.File
	name  string
	inode pinInode
}

// validatePinCustody is independent of the original mutation union: caller SH
// must survive return after those EX locks release. A later handoff needs fresh
// root authority and a newly acquired complete union as well as this check.
func (pin *usePin) validatePinCustody(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if pin == nil || pin.owner == nil || pin.file == nil || pin.inode.inode == 0 {
		return errors.New("local: pin reservation unavailable")
	}
	actual, err := openedPinInode(pin.file)
	if err != nil || actual != pin.inode {
		return errors.Join(errors.New("local: held pin identity changed"), err)
	}
	if err := pin.owner.validateFileCustody(pin.file, pin.name); err != nil {
		return err
	}
	return ctx.Err()
}

// reserveExistingPin never creates, truncates, adopts by name or repairs a pin.
// Its expected inode must come from the original trusted control record. New
// creation uses the separate grant/durable-intent protocol and never this path.
func (p *ports) reserveExistingPin(ctx context.Context, key workspace.LockKey, expected pinInode, union *heldUnion) (*usePin, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if expected.inode == 0 || union == nil || union.owner != p || !slices.Contains(union.keys, key) {
		return nil, errors.New("local: recorded pin and held identity mutation resource required")
	}
	if err := union.validate(); err != nil {
		return nil, err
	}
	name := "pin-" + digestName(key.CanonicalID)
	open := func() (*os.File, error) {
		info, err := p.control.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !privateNative(info, false) {
			return nil, errors.New("local: pin custody unavailable")
		}
		file, err := p.control.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		inode, identityErr := openedPinInode(file)
		if err = errors.Join(identityErr, p.validateFileCustody(file, name)); err != nil || inode != expected {
			return nil, errors.Join(errors.New("local: original pin inode changed"), err, file.Close())
		}
		return file, nil
	}
	probe, err := open()
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(err, probe.Close())
	}
	// Release the temporary EX descriptor while the complete mutation EX union
	// remains held. A distinct descriptor subsequently obtains SH on SAME inode.
	err = errors.Join(union.validate(), p.validateFileCustody(probe, name), ctx.Err())
	err = errors.Join(err, syscall.Flock(int(probe.Fd()), syscall.LOCK_UN), probe.Close())
	if err != nil {
		return nil, err
	}
	if err = union.validate(); err != nil {
		return nil, err
	}
	shared, err := open()
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(shared.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(err, shared.Close())
	}
	err = errors.Join(union.validate(), p.validateFileCustody(shared, name), ctx.Err())
	if err != nil {
		return nil, errors.Join(err, syscall.Flock(int(shared.Fd()), syscall.LOCK_UN), shared.Close())
	}
	return &usePin{owner: p, union: union, file: shared, name: name, inode: expected}, nil
}
