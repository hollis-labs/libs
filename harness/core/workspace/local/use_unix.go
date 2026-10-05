//go:build linux || darwin

package local

import (
	"context"
	"errors"
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
// pin creation remains unsupported until explicit grant + durable intent wiring.
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
