package snapshot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

var (
	ErrCaptureDisabled      = errors.New("snapshot: capture disabled without finite policy")
	ErrAdmissionUnavailable = errors.New("snapshot: protected admission unavailable")
	ErrSnapshotBudget       = errors.New("snapshot: finite budget exhausted; evidence retained")
	ErrSnapshotPinned       = errors.New("snapshot: referenced objects retained")
)

// CaptureBudgets bounds one capture, all retained storage and a run. Zero is
// never unlimited. These are host-selected limits, not application defaults.
type CaptureBudgets struct {
	MaxCaptureBytes, MaxRootBytes, MaxStorageBytes, MaxRunBytes int64
	MaxCaptureEntries, MaxRoots, MaxRunCaptures                 int
	MaxDuration                                                 time.Duration
}

// RetentionPolicy defaults to KEEP. Expiration selects only unpinned sets;
// it never expires an owner's journal/export/fork references.
type RetentionPolicy struct {
	MaxAge          time.Duration
	MaxSnapshotSets int
}

type CapturePolicy struct {
	Revision  string
	Budgets   CaptureBudgets
	Retention RetentionPolicy
}

func (p *CapturePolicy) Validate() error {
	if p == nil {
		return ErrCaptureDisabled
	}
	b := p.Budgets
	if p.Revision == "" || b.MaxCaptureBytes <= 0 || b.MaxRootBytes <= 0 || b.MaxStorageBytes <= 0 || b.MaxRunBytes <= 0 || b.MaxCaptureEntries <= 0 || b.MaxRoots <= 0 || b.MaxRunCaptures <= 0 || b.MaxDuration <= 0 || b.MaxRootBytes > b.MaxCaptureBytes || b.MaxCaptureBytes > b.MaxStorageBytes || b.MaxCaptureBytes > b.MaxRunBytes || p.Retention.MaxAge < 0 || p.Retention.MaxSnapshotSets < 0 {
		return ErrCaptureDisabled
	}
	return nil
}

// CaptureIntent binds an operation to its resolved inputs. None of these IDs
// grants read, write, isolation or deletion authority.
type CaptureIntent struct {
	SetID, OperationID, RunID, InstanceID, InputDigest, TargetMapDigest string
	PolicyRevision, BindingFence, BootGeneration, RuntimeGeneration     string
	ControllerEpoch                                                     uint64
}

func (i CaptureIntent) validate() error {
	if i.ControllerEpoch == 0 || len(i.InputDigest) != 64 || len(i.TargetMapDigest) != 64 || !validObjectID(i.InputDigest) || !validObjectID(i.TargetMapDigest) {
		return ErrAdmissionUnavailable
	}
	for _, s := range []string{i.SetID, i.OperationID, i.RunID, i.InstanceID, i.InputDigest, i.TargetMapDigest, i.PolicyRevision, i.BindingFence, i.BootGeneration, i.RuntimeGeneration} {
		if strings.TrimSpace(s) == "" || len(s) > 256 || strings.ContainsRune(s, 0) {
			return ErrAdmissionUnavailable
		}
	}
	return nil
}

type PinKind string

const (
	PinJournal PinKind = "journal"
	PinExport  PinKind = "export"
	PinFork    PinKind = "fork"
)

func (p PinKind) valid() bool { return p == PinJournal || p == PinExport || p == PinFork }

type pinRecord struct {
	Owner, Completion string
	Kind              PinKind
	SetID             string
}
type runAccount struct {
	Captures int
	Bytes    int64
}
type setAccount struct {
	Intent         CaptureIntent
	PolicyRevision string
	Set            SnapshotSet
	Bytes          int64
	Pending        bool
	Digest         string
	Manifest       RetainedManifest
}
type admissionLedger struct {
	Version                  int
	StoreID                  string
	StorePath, StoreIdentity string
	Sets                     map[string]setAccount
	Pins                     map[string]pinRecord
	Runs                     map[string]runAccount
	StorageBytes             int64
}

// Admission is issued only by a guarded provider after its store/isolation
// checks. There is deliberately no public constructor or decoded capability.
// Its stable lock serializes capture, complete-set pin admission, reads and GC.
type Admission struct {
	shadow    *ShadowGit
	guard     *storeGuard
	root      string
	directory *os.Root
	identity  os.FileInfo
	ancestry  map[string]os.FileInfo
	policy    CapturePolicy
	storeID   string
}

// RetainedSet is an issued data receipt. JSON or caller-created values cannot
// acquire a read lease; revalidation uses the complete durable ledger.
type RetainedSet struct {
	admission     *Admission
	setID, digest string
}

func (r *RetainedSet) ID() string {
	if r == nil {
		return ""
	}
	return r.setID
}
func (r *RetainedSet) StoreID() string {
	if r == nil || r.admission == nil {
		return ""
	}
	return r.admission.storeID
}

// CaptureLease remains held through confidential mirror creation, Git object
// ingestion and final accounting. Interrupted captures retain their budget
// reservation; they cannot be silently retried or reminted.
type CaptureLease struct {
	admission *Admission
	held      *admissionLock
	ledger    admissionLedger
	intent    CaptureIntent
	deadline  time.Time
	finished  bool
}

func (l *CaptureLease) Budgets() CaptureBudgets {
	if l == nil || l.admission == nil {
		return CaptureBudgets{}
	}
	return l.admission.policy.Budgets
}
func (l *CaptureLease) check(ctx context.Context) error {
	if l == nil || l.admission == nil || l.held == nil || l.finished {
		return ErrAdmissionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(l.deadline) {
		return ErrSnapshotBudget
	}
	return l.held.check()
}

func newAdmission(root string, policy *CapturePolicy) (*Admission, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	a, err := openAdmission(root, *policy)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), policy.Budgets.MaxDuration)
	defer cancel()
	h, err := a.lock(ctx)
	if err != nil {
		a.directory.Close()
		return nil, err
	}
	defer h.close()
	state, err := a.load()
	if errors.Is(err, os.ErrNotExist) {
		dir, e := a.directory.Open(".")
		if e != nil {
			a.directory.Close()
			return nil, e
		}
		entries, e := dir.ReadDir(-1)
		e = errors.Join(e, dir.Close())
		if e != nil {
			a.directory.Close()
			return nil, e
		}
		for _, entry := range entries {
			if entry.Name() != "admission.lock" {
				a.directory.Close()
				return nil, ErrAdmissionUnavailable
			}
		}
		id, e := randomAdmissionID()
		if e != nil {
			return nil, e
		}
		state = admissionLedger{Version: 1, StoreID: id, StorePath: a.root, StoreIdentity: a.storeOriginIdentity(), Sets: map[string]setAccount{}, Pins: map[string]pinRecord{}, Runs: map[string]runAccount{}}
		if err = a.save(h, state); err != nil {
			a.directory.Close()
			return nil, err
		}
	} else if err != nil {
		a.directory.Close()
		return nil, err
	}
	a.storeID = state.StoreID
	return a, nil
}

func randomAdmissionID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func (a *Admission) beginCapture(ctx context.Context, intent CaptureIntent) (*CaptureLease, error) {
	if a == nil || intent.PolicyRevision != a.policy.Revision {
		return nil, ErrAdmissionUnavailable
	}
	if err := intent.validate(); err != nil {
		return nil, err
	}
	h, err := a.lock(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*CaptureLease, error) { return nil, errors.Join(e, h.close()) }
	state, err := a.load()
	if err != nil {
		return fail(err)
	}
	if state.StoreID != a.storeID {
		return fail(ErrAdmissionUnavailable)
	}
	if _, ok := state.Sets[intent.SetID]; ok {
		return fail(ErrAdmissionUnavailable)
	}
	b := a.policy.Budgets
	run := state.Runs[intent.RunID]
	// Reserve before even reading candidate content. No GC to make room and no
	// renewal after uncertain ingestion. Arithmetic uses subtraction to avoid overflow.
	if state.StorageBytes > b.MaxStorageBytes-b.MaxCaptureBytes || run.Bytes > b.MaxRunBytes-b.MaxCaptureBytes || run.Captures >= b.MaxRunCaptures {
		return fail(ErrSnapshotBudget)
	}
	run.Captures++
	run.Bytes += b.MaxCaptureBytes
	state.Runs[intent.RunID] = run
	state.StorageBytes += b.MaxCaptureBytes
	state.Sets[intent.SetID] = setAccount{Intent: intent, PolicyRevision: a.policy.Revision, Bytes: b.MaxCaptureBytes, Pending: true}
	if err = a.save(h, state); err != nil {
		return fail(err)
	}
	return &CaptureLease{admission: a, held: h, ledger: state, intent: intent, deadline: time.Now().Add(b.MaxDuration)}, nil
}

func detachedSet(s SnapshotSet) SnapshotSet {
	out := s
	out.Roots = make(map[string]RootSnapshot, len(s.Roots))
	for id, r := range s.Roots {
		r.Skipped = append([]SkippedPath(nil), r.Skipped...)
		out.Roots[id] = r
	}
	return out
}
func setDigest(s SnapshotSet) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// finish is a same-package boundary used by the guarded capture implementation,
// after actual tree/object verification and bounded store measurement.
func (l *CaptureLease) finish(ctx context.Context, set SnapshotSet, bytes int64, manifest RetainedManifest) (*RetainedSet, error) {
	if err := l.check(ctx); err != nil {
		return nil, err
	}
	b := l.Budgets()
	if bytes < 0 || bytes > b.MaxCaptureBytes || len(set.Roots) == 0 || len(set.Roots) > b.MaxRoots || set.CapturedAt.IsZero() {
		return nil, ErrSnapshotBudget
	}
	set = detachedSet(set)
	set.ID = l.intent.SetID
	for id, r := range set.Roots {
		if id == "" || r.TargetID != id || r.Root == "" || r.Err != nil || !validObjectID(r.TreeHash) || !validObjectID(r.CommitHash) {
			return nil, ErrAdmissionUnavailable
		}
	}
	manifest = detachedManifest(manifest)
	if err := manifest.validate(set, l.admission.storeID); err != nil {
		return nil, err
	}
	s := l.ledger.Sets[l.intent.SetID]
	reserved := s.Bytes
	s.Set = set
	s.Bytes = bytes
	s.Pending = false
	s.Manifest = manifest
	digest, err := accountDigest(l.admission.storeID, s)
	if err != nil {
		return nil, err
	}
	s.Digest = digest
	l.ledger.Sets[l.intent.SetID] = s
	l.ledger.StorageBytes -= reserved - bytes
	run := l.ledger.Runs[l.intent.RunID]
	run.Bytes -= reserved - bytes
	l.ledger.Runs[l.intent.RunID] = run
	// Initial journal retention exists BEFORE the issued set can leave capture.
	key := pinKey(l.intent.SetID, l.intent.OperationID, PinJournal)
	l.ledger.Pins[key] = pinRecord{Owner: l.intent.OperationID, Kind: PinJournal, SetID: l.intent.SetID}
	if err = l.admission.save(l.held, l.ledger); err != nil {
		return nil, err
	}
	receipt := &RetainedSet{admission: l.admission, setID: l.intent.SetID, digest: digest}
	l.finished = true
	err = l.held.close()
	l.held = nil
	return receipt, err
}
func (l *CaptureLease) retainUncertain() error {
	if l == nil || l.held == nil {
		return nil
	}
	l.finished = true
	err := l.held.close()
	l.held = nil
	return err
}
func validObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}
func pinKey(set, owner string, kind PinKind) string {
	b, _ := json.Marshal([]string{set, owner, string(kind)})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ReadLease pins the ENTIRE multi-root set and keeps its admission lock held.
// Close retains the durable pin. Only Complete records explicit owner completion;
// acknowledgments, deadlines and process death never release references.
type ReadLease struct {
	admission *Admission
	held      *admissionLock
	set       setAccount
	pin       string
	active    bool
	deadline  time.Time
}

func (r *RetainedSet) Pin(ctx context.Context, owner string, kind PinKind) (*ReadLease, error) {
	if r == nil || r.admission == nil || owner == "" || !kind.valid() {
		return nil, ErrAdmissionUnavailable
	}
	a := r.admission
	h, err := a.lock(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*ReadLease, error) { return nil, errors.Join(e, h.close()) }
	state, err := a.load()
	if err != nil {
		return fail(err)
	}
	set, ok := state.Sets[r.setID]
	if !ok || set.Pending || set.Digest != r.digest || state.StoreID != a.storeID {
		return fail(ErrAdmissionUnavailable)
	}
	key := pinKey(r.setID, owner, kind)
	if p, ok := state.Pins[key]; ok && p.Completion != "" {
		return fail(ErrAdmissionUnavailable)
	}
	state.Pins[key] = pinRecord{Owner: owner, Kind: kind, SetID: r.setID}
	if err = a.save(h, state); err != nil {
		return fail(err)
	}
	return &ReadLease{admission: a, held: h, set: set, pin: key, active: true, deadline: time.Now().Add(a.policy.Budgets.MaxDuration)}, nil
}
func (l *ReadLease) Verify(ctx context.Context) error {
	if ctx == nil || l == nil || !l.active || l.held == nil {
		return ErrAdmissionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(l.deadline) {
		return ErrSnapshotBudget
	}
	if err := l.held.check(); err != nil {
		return err
	}
	state, err := l.admission.load()
	if err != nil {
		return err
	}
	s, ok := state.Sets[l.set.Intent.SetID]
	p, pok := state.Pins[l.pin]
	if state.StoreID != l.admission.storeID || !ok || s.Pending || s.Digest != l.set.Digest || !pok || p.Completion != "" {
		return ErrAdmissionUnavailable
	}
	return nil
}
func (l *ReadLease) Set() SnapshotSet {
	if l == nil {
		return SnapshotSet{}
	}
	return detachedSet(l.set.Set)
}
func (l *ReadLease) Intent() CaptureIntent {
	if l == nil {
		return CaptureIntent{}
	}
	return l.set.Intent
}
func (l *ReadLease) StoreID() string {
	if l == nil || l.admission == nil {
		return ""
	}
	return l.admission.storeID
}
func (l *ReadLease) Close() error {
	if l == nil || l.held == nil {
		return nil
	}
	l.active = false
	err := l.held.close()
	l.held = nil
	return err
}
func (l *ReadLease) Complete(ctx context.Context, completion *OwnerCompletion) error {
	if l == nil {
		return ErrAdmissionUnavailable
	}
	return completeReadLease(ctx, l, completion)
}

func (a *Admission) load() (admissionLedger, error) {
	var state admissionLedger
	file, err := a.openLedger()
	if err != nil {
		return state, err
	}
	defer file.Close()
	if err = a.checkFile(file, "admission.json"); err != nil {
		return state, err
	}
	dec := json.NewDecoder(io.LimitReader(file, 16<<20+1))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&state); err != nil {
		return state, ErrAdmissionUnavailable
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return state, ErrAdmissionUnavailable
	}
	if state.Version != 1 || state.StoreID == "" || state.StorePath != a.root || state.StoreIdentity != a.storeOriginIdentity() || state.Sets == nil || state.Pins == nil || state.Runs == nil || state.StorageBytes < 0 {
		return state, ErrAdmissionUnavailable
	}
	if err = a.validateLedger(state); err != nil {
		return state, err
	}
	return state, nil
}
func (a *Admission) save(h *admissionLock, state admissionLedger) error {
	if err := h.check(); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return ErrSnapshotBudget
	}
	name, err := randomAdmissionID()
	if err != nil {
		return err
	}
	name = ".admission-" + name
	f, err := a.directory.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	// A failed/lost-custody write retains its own private temp. No cleanup through
	// a replaced store path, and no truncation of an unowned existing ledger.
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = h.check(); err != nil {
		return err
	}
	if err = a.directory.Rename(name, "admission.json"); err != nil {
		return err
	}
	return a.syncDirectory()
}
func (a *Admission) String() string {
	if a == nil {
		return "snapshot admission unavailable"
	}
	return fmt.Sprintf("snapshot admission %s", a.storeID)
}
