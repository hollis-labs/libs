// Package local provides opt-in ports for protected local filesystems. Callers
// supply ownership and live fabric authority explicitly; paths grant neither.
// No constructor creates storage or discovers a home, registry or credential.
// Locks use exclusive flock on Linux and Darwin and refuse other platforms.
// Receipts synchronize ID bindings, sync their file before confined rename and
// sync the control directory before reporting success. Close requires no active
// operations or held locks. Existing directories retain their modes.
package local

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace"
)

// Options is specific to one operation and its resolved resource set. The
// pre-existing control root must be private and physically canonical, outside
// all workspaces. LocalFilesystem is the host's explicit filesystem-contract
// assertion, not a probe or support claim for arbitrary network filesystems.
type Options struct {
	OperationID       string
	ControlRoot       workspace.RootRef
	Resources         workspace.Resources
	LocalFilesystem   bool
	ValidateAuthority func(context.Context, workspace.Spec, workspace.Resources) error
	Evidence          func(context.Context) (workspace.Observations, error)
	Clock             workspace.Clock
}

type ports struct {
	options Options
	control *os.Root
}
type clock struct{}

func (clock) Now() time.Time { return time.Now() }

// New opens already-authorized control storage. Close must be called after all
// operations and held locks finish. Managed files are never written here.
func New(options Options) (workspace.Ports, func() error, error) {
	if !options.LocalFilesystem || options.OperationID == "" || options.ValidateAuthority == nil || options.Evidence == nil {
		return workspace.Ports{}, nil, errors.New("local: explicit operation, local filesystem and authority required")
	}
	o, err := workspace.InspectRoot(options.ControlRoot)
	if err != nil {
		return workspace.Ports{}, nil, err
	}
	if !o.Exists || !o.Directory {
		return workspace.Ports{}, nil, errors.New("local: pre-existing control directory required")
	}
	info, err := os.Stat(options.ControlRoot.Path)
	if err != nil {
		return workspace.Ports{}, nil, err
	}
	if info.Mode().Perm() != 0700 {
		return workspace.Ports{}, nil, errors.New("local: control directory must be private")
	}
	if options.Resources.LockNamespace != options.ControlRoot.Path || options.Resources.LockRoot != options.ControlRoot {
		return workspace.Ports{}, nil, errors.New("local: lock namespace must be control root")
	}
	for _, r := range options.Resources.Roots {
		if r.Owner != options.ControlRoot.Owner {
			return workspace.Ports{}, nil, errors.New("local: control and workspace ownership differ")
		}
		rel, err := filepath.Rel(r.Path, options.ControlRoot.Path)
		if err != nil || rel == "." || filepath.IsLocal(rel) {
			return workspace.Ports{}, nil, errors.New("local: control root overlaps workspace")
		}
		rel, err = filepath.Rel(options.ControlRoot.Path, r.Path)
		if err != nil || rel == "." || filepath.IsLocal(rel) {
			return workspace.Ports{}, nil, errors.New("local: workspace overlaps control root")
		}
	}
	resourcesData, err := json.Marshal(options.Resources)
	if err != nil {
		return workspace.Ports{}, nil, err
	}
	if err = json.Unmarshal(resourcesData, &options.Resources); err != nil {
		return workspace.Ports{}, nil, err
	}
	root, err := os.OpenRoot(options.ControlRoot.Path)
	if err != nil {
		return workspace.Ports{}, nil, err
	}
	if options.Clock == nil {
		options.Clock = clock{}
	}
	p := &ports{options: options, control: root}
	return workspace.Ports{Clock: options.Clock, Host: p, Locks: p, Observations: p, ReceiptStore: p}, root.Close, nil
}
func (p *ports) Validate(ctx context.Context, s workspace.Spec, r workspace.Resources) error {
	if s.OperationID != p.options.OperationID || !sameResources(r, p.options.Resources) {
		return errors.New("local: resource or operation authorization mismatch")
	}
	return p.options.ValidateAuthority(ctx, s, r)
}
func sameResources(a, b workspace.Resources) bool {
	// Plan canonicalizes set order. Compare canonicalized detached records.
	norm := func(r workspace.Resources) workspace.Resources {
		r.Roots = slices.Clone(r.Roots)
		slices.SortFunc(r.Roots, func(a, b workspace.RootRef) int { return compare(a.ID, b.ID) })
		r.Grants = slices.Clone(r.Grants)
		slices.SortFunc(r.Grants, func(a, b workspace.EffectGrant) int {
			if n := compare(string(a.Kind), string(b.Kind)); n != 0 {
				return n
			}
			if n := compare(a.RootID, b.RootID); n != 0 {
				return n
			}
			if n := compare(a.AuthorizationID, b.AuthorizationID); n != 0 {
				return n
			}
			return compare(a.Version, b.Version)
		})
		r.Grants = slices.Compact(r.Grants)
		r.Capabilities = slices.Clone(r.Capabilities)
		slices.Sort(r.Capabilities)
		r.Capabilities = slices.Compact(r.Capabilities)
		r.ProviderHomes = slices.Clone(r.ProviderHomes)
		slices.SortFunc(r.ProviderHomes, func(a, b workspace.ResourceRef) int {
			aa, _ := json.Marshal(a)
			bb, _ := json.Marshal(b)
			return compare(string(aa), string(bb))
		})
		r.ProviderHomes = slices.Compact(r.ProviderHomes)
		return r
	}
	return reflect.DeepEqual(norm(a), norm(b))
}
func compare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
func (p *ports) owned(r workspace.RootRef) bool {
	for _, allowed := range p.options.Resources.Roots {
		if r == allowed {
			return true
		}
	}
	return false
}
func (p *ports) EnsureOwnedDirectory(ctx context.Context, r workspace.RootRef, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.owned(r) || mode != 0700 {
		return errors.New("local: directory authority required")
	}
	o, err := workspace.InspectRoot(r)
	if err != nil {
		return err
	}
	if o.Exists {
		return nil
	}
	// Only this explicit directory is created. Unknown parent chains are not
	// adopted or fabricated; the caller must provide the containing resource.
	parent, err := os.OpenRoot(filepath.Dir(r.Path))
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(r.Path)
	if err = parent.Mkdir(name, mode); err != nil {
		return err
	}
	return parent.Chmod(name, mode)
}
func (p *ports) Observe(ctx context.Context, r workspace.Resources) (workspace.Observations, error) {
	if !sameResources(r, p.options.Resources) {
		return workspace.Observations{}, errors.New("local: observation resource mismatch")
	}
	o, err := p.options.Evidence(ctx)
	if err != nil {
		return o, err
	}
	o.Roots = nil
	for _, root := range append(slices.Clone(r.Roots), r.LockRoot) {
		if err := ctx.Err(); err != nil {
			return o, err
		}
		observed, err := workspace.InspectRoot(root)
		if err != nil {
			return o, err
		}
		o.Roots = append(o.Roots, observed)
	}
	receipt, err := p.readReceipt()
	if err == nil {
		o.Receipts = append(o.Receipts, receipt)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return o, err
	}
	return o, nil
}
func digestName(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (p *ports) readReceipt() (workspace.Receipt, error) {
	name := "receipt-" + digestName(p.options.OperationID) + ".json"
	info, err := p.control.Lstat(name)
	if err != nil {
		return workspace.Receipt{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return workspace.Receipt{}, errors.New("local: invalid receipt file")
	}
	file, err := p.control.Open(name)
	if err != nil {
		return workspace.Receipt{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil {
		return workspace.Receipt{}, err
	}
	var receipt workspace.Receipt
	if err = json.Unmarshal(data, &receipt); err != nil {
		return receipt, errors.New("local: invalid receipt encoding")
	}
	return receipt, nil
}
func (p *ports) Record(ctx context.Context, r workspace.Receipt) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.OperationID != p.options.OperationID || r.SchemaVersion != workspace.SchemaVersion || r.InputDigest == "" {
		return errors.New("local: invalid operation receipt")
	}
	// A distinct, context-aware file lock protects the ID-to-input binding across
	// callers and processes. It is control storage, not an artifact mutation lock.
	lock, err := p.acquireFile(ctx, "receipt-lock-"+digestName(r.OperationID))
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	prior, err := p.readReceipt()
	if err == nil {
		if prior.SchemaVersion != workspace.SchemaVersion || prior.OperationID != r.OperationID || prior.InputDigest != r.InputDigest || prior.IdentityKey != r.IdentityKey {
			return errors.New("local: operation ID already bound")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// Control files alone use confined temporary writes and rename. Managed
	// artifacts and engine manifests still belong to the concrete engine.
	name := "receipt-" + digestName(r.OperationID) + ".json"
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := "." + name + "." + hex.EncodeToString(nonce[:]) + ".new"
	file, err := p.control.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer p.control.Remove(tmp)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = p.control.Rename(tmp, name); err != nil {
		return err
	}
	// The name binding must be durable before callers begin artifact mutation.
	// A sync error is a failed Record even when the renamed file is visible.
	directory, err := p.control.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
func (p *ports) Acquire(ctx context.Context, k workspace.LockKey) (workspace.HeldLock, error) {
	if k.Namespace != p.options.ControlRoot.Path {
		return nil, errors.New("local: lock namespace mismatch")
	}
	allowed := false
	for _, r := range p.options.Resources.Roots {
		if r.Path == k.CanonicalID {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("local: unknown canonical lock identity")
	}
	return p.acquireFile(ctx, "lock-"+digestName(k.CanonicalID))
}
