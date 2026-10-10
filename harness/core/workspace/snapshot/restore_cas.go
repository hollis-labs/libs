package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var (
	ErrRestoreConflict  = errors.New("snapshot: expected current state conflicts")
	ErrRestoreUncertain = errors.New("snapshot: restore effects uncertain; admission and references retained")
)

type ExpectedFile struct {
	Present bool
	SHA256  string
}
type RestoreSelection struct {
	TargetID, Path string
	Expected       ExpectedFile
}
type RestoreRequest struct {
	Intent     CaptureIntent
	Selections []RestoreSelection
}
type RestoreResult struct {
	OperationID, Outcome string
	ChangedCount         int
	Partial              bool
	Obligations          []string
}

type restoreRecord struct {
	OperationID, RequestDigest, ReceiptDigest, Phase string
	Changed                                          int
	Stages                                           []restoreStage
	History                                          []restoreStep
}
type restoreStage struct {
	TargetID, Path, Name, ExpectedHash, CapturedHash string
	Present                                          bool
}
type restoreStep struct {
	Phase   string
	Changed int
}
type restoreFile struct {
	selection   RestoreSelection
	scope       plannedRoot
	captured    CapturedFile
	root        *os.Root
	parent      *os.File
	name, stage string
	expected    os.FileInfo
}

// RestoreSelective restores captured regular files only. Every selection is
// preflighted before any staging or replacement. Actual frozen source isolation
// and the independent host's continuous writer/submission fence are mandatory;
// advisory store locking alone is never a current-content CAS guarantee.
// Native effects are journaled before they happen and never automatically replay.
func (p *GuardedProvider) RestoreSelective(ctx context.Context, retained *RetainedSet, request RestoreRequest) (result RestoreResult, err error) {
	result.OperationID = request.Intent.OperationID
	result.Outcome = "refused"
	if ctx == nil || p == nil || p.admission == nil || p.isolation == nil || retained == nil || retained.admission != p.admission || request.Intent.validate() != nil || request.Intent.SetID != retained.ID() || request.Intent.TargetMapDigest != p.plan.Digest() || request.Intent.PolicyRevision != p.admission.policy.Revision {
		return result, ErrAdmissionUnavailable
	}
	if !p.operationMu.TryLock() {
		return result, ErrAdmissionUnavailable
	}
	defer p.operationMu.Unlock()
	request.Selections = append([]RestoreSelection(nil), request.Selections...)
	if len(request.Selections) == 0 || len(request.Selections) > p.admission.policy.Budgets.MaxCaptureEntries {
		return result, ErrCoverageUnsupported
	}
	encoded, e := json.Marshal(request)
	if e != nil || len(encoded) > 1<<20 {
		return result, ErrSnapshotBudget
	}
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	op := p.operation("restore", request.Intent, digest)
	host, e := p.acquireHost(ctx, op)
	if e != nil {
		return result, e
	}
	pinned := false
	defer func() {
		if closeErr := closeSnapshotAdmission(host); closeErr != nil {
			err = errors.Join(err, ErrRestoreUncertain)
			result.Outcome = "uncertain"
			result.Partial = true
			result.Obligations = restoreObligations(pinned)
		}
	}()
	lease, e := retained.pin(ctx, request.Intent.OperationID, PinFork, false)
	if e != nil {
		return result, e
	}
	pinned = true
	defer func() { err = errors.Join(err, lease.Close()) }()
	verify := func() error {
		if e := lease.Verify(ctx); e != nil {
			return e
		}
		return p.verifyHost(ctx, host, op)
	}
	if e = verify(); e != nil {
		return result, e
	}
	var files []*restoreFile
	defer func() {
		for _, f := range files {
			if f.parent != nil {
				f.parent.Close()
			}
			if f.root != nil {
				f.root.Close()
			}
		}
	}()
	byRoot := map[string]map[string]CapturedFile{}
	var selectedBytes int64
	seen := map[string]bool{}
	for _, selection := range request.Selections {
		if !safeCapturedPath(selection.Path) || !safeReceiptID(selection.TargetID) || seen[selection.TargetID+"\x00"+selection.Path] || (!selection.Expected.Present && selection.Expected.SHA256 != "") || (selection.Expected.Present && (len(selection.Expected.SHA256) != 64 || !validObjectID(selection.Expected.SHA256))) {
			return result, ErrCoverageUnsupported
		}
		seen[selection.TargetID+"\x00"+selection.Path] = true
		var scope plannedRoot
		found := false
		for _, candidate := range p.plan.roots {
			if candidate.Binding.ID == selection.TargetID {
				scope = candidate
				found = true
				break
			}
		}
		if !found || !scope.allows(filepath.Join(scope.Binding.Root, selection.Path)) || SecretExcluded(selection.Path) {
			return result, ErrCoverageUnsupported
		}
		if byRoot[selection.TargetID] == nil {
			read, e := lease.ReadFiles(ctx, selection.TargetID)
			if e != nil {
				return result, e
			}
			byRoot[selection.TargetID] = map[string]CapturedFile{}
			for _, file := range read {
				byRoot[selection.TargetID][file.Path] = file
			}
		}
		captured, ok := byRoot[selection.TargetID][selection.Path]
		if !ok {
			return result, ErrCoverageUnsupported
		}
		if int64(len(captured.Bytes)) > p.admission.policy.Budgets.MaxCaptureBytes-selectedBytes {
			return result, ErrSnapshotBudget
		}
		selectedBytes += int64(len(captured.Bytes))
		root, e := os.OpenRoot(scope.Binding.Root)
		if e != nil {
			return result, ErrCoverageUnsupported
		}
		f := &restoreFile{selection: selection, scope: scope, captured: captured, root: root, name: filepath.Base(selection.Path)}
		files = append(files, f)
		f.parent, e = openRestoreParent(root, filepath.Dir(selection.Path))
		if e != nil {
			return result, e
		}
		if e = p.checkRestoreFile(f); e != nil {
			return result, e
		}
	}
	if e = verify(); e != nil {
		return result, e
	}
	// This record is private sanitized evidence, not a decoded capability. An
	// existing operation refuses even if its previous effect outcome is unknown.
	record := restoreRecord{OperationID: request.Intent.OperationID, RequestDigest: digest, ReceiptDigest: retained.digest, Phase: "prepared"}
	for _, file := range files {
		id, e := randomAdmissionID()
		if e != nil {
			return result, ErrAdmissionUnavailable
		}
		file.stage = ".snapshot-restore-" + id
		sum := sha256.Sum256(file.captured.Bytes)
		record.Stages = append(record.Stages, restoreStage{TargetID: file.selection.TargetID, Path: file.selection.Path, Name: file.stage, ExpectedHash: file.selection.Expected.SHA256, CapturedHash: hex.EncodeToString(sum[:]), Present: file.selection.Expected.Present})
	}
	record.History = append(record.History, restoreStep{Phase: record.Phase})
	idsum := sha256.Sum256([]byte(request.Intent.OperationID))
	recordName := "restore-" + hex.EncodeToString(idsum[:]) + ".json"
	if e = writeRestoreRecord(lease, recordName, record, true); e != nil {
		return result, e
	}
	started := true
	defer func() {
		if err != nil && started {
			result.Outcome = "uncertain"
			result.Partial = true
			result.Obligations = restoreObligations(pinned)
			err = errors.Join(err, ErrRestoreUncertain)
		}
	}()
	if e = host.Record(ctx, op, SnapshotEffect{Phase: "restore_intent", Outcome: "pending"}); e != nil {
		return result, ErrAdmissionUnavailable
	}
	for _, f := range files {
		if e = verify(); e != nil {
			return result, e
		}
		if e = stageRestoreFile(f); e != nil {
			return result, e
		}
	}
	// Repeat the complete preflight after staging and before the first replace.
	for _, f := range files {
		if e = p.checkRestoreFile(f); e != nil {
			return result, e
		}
	}
	if e = verify(); e != nil {
		return result, e
	}
	for _, f := range files {
		if e = verify(); e != nil {
			return result, e
		}
		if e = p.checkRestoreFile(f); e != nil {
			return result, e
		}
		record.Phase = "replace_intent"
		record.Changed = result.ChangedCount
		record.History = append(record.History, restoreStep{record.Phase, record.Changed})
		if e = writeRestoreRecord(lease, recordName, record, false); e != nil {
			return result, e
		}
		if e = replaceRestoreFile(f); e != nil {
			return result, e
		}
		result.ChangedCount++
		if e = f.parent.Sync(); e != nil {
			return result, ErrRestoreUncertain
		}
		if e = verify(); e != nil {
			return result, e
		}
		if e = host.Record(ctx, op, SnapshotEffect{Phase: "file_replaced", Outcome: "pending", Changed: result.ChangedCount, Partial: true}); e != nil {
			return result, ErrAdmissionUnavailable
		}
		record.Phase = "file_replaced"
		record.Changed = result.ChangedCount
		record.History = append(record.History, restoreStep{record.Phase, record.Changed})
		if e = writeRestoreRecord(lease, recordName, record, false); e != nil {
			return result, e
		}
	}
	if e = verify(); e != nil {
		return result, e
	}
	if e = host.Record(ctx, op, SnapshotEffect{Phase: "restore_complete", Outcome: "complete", Changed: result.ChangedCount}); e != nil {
		return result, ErrAdmissionUnavailable
	}
	record.Phase = "complete"
	record.Changed = result.ChangedCount
	record.History = append(record.History, restoreStep{record.Phase, record.Changed})
	if e = writeRestoreRecord(lease, recordName, record, false); e != nil {
		return result, e
	}
	started = false
	result.Outcome = "complete"
	result.Obligations = []string{"retained_set_pinned"}
	return result, nil
}

func (p *GuardedProvider) checkRestoreFile(f *restoreFile) error {
	max := p.admission.policy.Budgets.MaxRootBytes
	// Resolve only immutable host-named credential metadata under the same
	// writer fence. A newly linked credential must refuse BEFORE hashing bytes.
	credentials, e := secretFileIdentities(p.plan.credentialPaths)
	if e != nil {
		return ErrCoverageUnsupported
	}

	current, e := os.Stat(f.scope.Binding.Root)
	held, e2 := f.root.Stat(".")
	if e != nil || e2 != nil || !physicalDirectory(f.scope.Binding.Root) || !os.SameFile(f.scope.identity, current) || !os.SameFile(current, held) {
		return ErrCoverageUnsupported
	}
	// A renamed parent must not turn a held descriptor into an unrelated target.
	parent, e := openRestoreParent(f.root, filepath.Dir(f.selection.Path))
	if e != nil {
		return e
	}
	info, e := parent.Stat()
	parent.Close()
	original, e2 := f.parent.Stat()
	if e != nil || e2 != nil || !os.SameFile(info, original) {
		return ErrCoverageUnsupported
	}
	file, e := openCaptureFile(f.root, f.selection.Path)
	if e != nil {
		if !f.selection.Expected.Present && errors.Is(restoreEntryError(f), os.ErrNotExist) {
			return nil
		}
		return ErrRestoreConflict
	}
	defer file.Close()
	info, e = file.Stat()
	if e != nil || !info.Mode().IsRegular() || !f.selection.Expected.Present || info.Size() > max {
		return ErrRestoreConflict
	}
	for _, credential := range credentials {
		if os.SameFile(info, credential) {
			return ErrCoverageUnsupported
		}
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(file, max+1))
	after, e2 := file.Stat()
	if e != nil || e2 != nil || n > max || n != info.Size() || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || hex.EncodeToString(h.Sum(nil)) != f.selection.Expected.SHA256 {
		return ErrRestoreConflict
	}
	if f.expected != nil && !os.SameFile(f.expected, info) {
		return ErrRestoreConflict
	}
	f.expected = info
	return nil
}
func restoreEntryError(f *restoreFile) error {
	info, e := f.root.Lstat(f.selection.Path)
	if e == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return ErrCoverageUnsupported
	}
	return e
}
func writeRestoreRecord(lease *ReadLease, name string, record restoreRecord, initial bool) error {
	if e := lease.Verify(context.Background()); e != nil {
		return e
	}
	data, e := json.Marshal(record)
	if e != nil {
		return ErrAdmissionUnavailable
	}
	if int64(len(data)) > lease.admission.policy.Budgets.MaxCaptureBytes {
		return ErrSnapshotBudget
	}
	provider := &GuardedProvider{guard: lease.admission.guard, admission: lease.admission}
	used, e := provider.measureStore(context.Background(), lease.admission.policy.Budgets.MaxCaptureEntries)
	if e != nil || used > lease.admission.policy.Budgets.MaxStorageBytes-int64(len(data)) {
		return ErrSnapshotBudget
	}
	target := name
	if !initial {
		id, e := randomAdmissionID()
		if e != nil {
			return ErrAdmissionUnavailable
		}
		target = ".restore-record-" + id
	}
	file, e := lease.admission.directory.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrAdmissionUnavailable
	}
	n, e := file.Write(data)
	if e == nil && n != len(data) {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = file.Sync()
	}
	e = errors.Join(e, file.Close())
	if e != nil {
		return ErrAdmissionUnavailable
	}
	if !initial {
		if e = lease.admission.directory.Rename(target, name); e != nil {
			return ErrAdmissionUnavailable
		}
	}
	return lease.admission.syncDirectory()
}

func restoreObligations(pinned bool) []string {
	pin := "retained_set_pin_required"
	if pinned {
		pin = "retained_set_pinned"
	}
	return []string{"restore_inspection_required", pin, "writer_admission_fenced"}
}
