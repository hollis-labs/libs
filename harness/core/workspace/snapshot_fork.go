package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"sort"

	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
)

// SnapshotForkRequest authorizes a separate, inactive new root. The source is
// an issued retained receipt, not a caller-decoded SnapshotSet. The ordinary
// workspace input/ports still supply all destination authority and mutation
// locks; neither the source receipt nor a path confers that authority.
type SnapshotForkRequest struct {
	Source   *snapshot.RetainedSet
	TargetID string
	Input    TreeRequest
}

type SnapshotForkReceipt struct {
	SourceSetID, SourceStoreID, SourceTargetID, SourceTreeHash, SourceCommitHash string
	Destination                                                                  RootRef
	OperationID, InputDigest                                                     string
}

// SnapshotForkResult always returns complete ApplyTree accounting on errors.
// The source pin remains retained until a supported durable owner-completion
// adapter can release it. No external effects, repository metadata or native
// conversation are rolled back, and no runtime-ready claim is made.
type SnapshotForkResult struct {
	Apply             ApplyResult
	Receipt           *SnapshotForkReceipt
	SourcePinRetained bool
}

func ForkSnapshot(ctx context.Context, request SnapshotForkRequest, ports Ports) (result SnapshotForkResult, err error) {
	if ctx == nil || request.Source == nil || request.TargetID == "" {
		return result, refuse("snapshot_fork_source_unavailable", "snapshot", Unsupported)
	}
	if e := ctx.Err(); e != nil {
		return result, e
	}
	input := request.Input
	if e := validateFrozenValues(input); e != nil {
		return result, e
	}
	input = copyRecord(input)
	if input.OperationID == "" || input.Operation != materialize.OperationCreate || len(input.Tree.Entries) != 0 || input.ExpectedGeneration != "" || len(input.Selection.EntryIDs) != 0 || len(input.Selection.Groups) != 0 {
		return result, refuse("snapshot_fork_requires_new_root", "snapshot", Conflict)
	}
	if e := ValidateTreeAuthority(input); e != nil {
		return result, e
	}
	for _, o := range input.Observed.Roots {
		if o.RootID == input.Root.ID && o.Exists {
			return result, refuse("snapshot_fork_destination_exists", "snapshot", Conflict)
		}
	}
	lease, e := request.Source.Pin(ctx, input.OperationID, snapshot.PinFork)
	if e != nil {
		return result, errors.Join(refuse("snapshot_fork_pin_unavailable", "snapshot", Unsupported), e)
	}
	result.SourcePinRetained = true
	defer func() {
		closeErr := lease.Close()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
			result.Receipt = nil
			result.Apply.Status = Partial
		}
	}()
	source := lease.Set()
	root, ok := source.Roots[request.TargetID]
	if !ok {
		return result, refuse("snapshot_fork_root_unavailable", "snapshot", Unsupported)
	}
	// A captured content tree is immutable, but it never authorizes using a live
	// source root, its ancestor or its descendants as the destination.
	if e = lease.ValidateForkDestination(ctx, input.Root.Path); e != nil {
		return result, e
	}
	for _, o := range input.Observed.Roots {
		if o.RootID == input.Root.ID {
			if e = lease.ValidateForkDestination(ctx, o.CanonicalPath); e != nil {
				return result, e
			}
		}
	}
	for _, r := range source.Roots {
		if forkPathsOverlap(input.Root.Path, r.Root) {
			return result, refuse("snapshot_fork_destination_overlaps_source", "snapshot", Conflict)
		}
	}
	files, e := lease.ReadFiles(ctx, request.TargetID)
	if e != nil {
		return result, errors.Join(refuse("snapshot_fork_read_unavailable", "snapshot", Unsupported), e)
	}
	input.Tree = snapshotForkTree(files, root.TreeHash)

	if e = lease.Verify(ctx); e != nil {
		return result, e
	}
	result.Apply, err = ApplyTree(ctx, input, ports)
	// Preserve every handle/obligation even if a late callback changed custody.
	if e = lease.Verify(ctx); e != nil {
		err = errors.Join(err, e)
		result.Apply.Status = Partial
		return result, err
	}
	if err != nil {
		return result, err
	}
	if !result.Apply.ArtifactsComplete() {
		return result, refuse("snapshot_fork_completion_unproved", "snapshot", Partial)
	}
	result.Receipt = &SnapshotForkReceipt{SourceSetID: source.ID, SourceStoreID: lease.StoreID(), SourceTargetID: request.TargetID, SourceTreeHash: root.TreeHash, SourceCommitHash: root.CommitHash, Destination: input.Root, OperationID: input.OperationID, InputDigest: result.Apply.Receipt.InputDigest}
	return result, nil
}
func forkPathsOverlap(a, b string) bool {
	if a == "" || b == "" || !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return true
	}
	rel, e := filepath.Rel(a, b)
	if e == nil && rel != ".." && !filepath.IsAbs(rel) && !(len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator)) {
		return true
	}
	rel, e = filepath.Rel(b, a)
	return e == nil && rel != ".." && !filepath.IsAbs(rel) && !(len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator))
}

func snapshotForkTree(files []snapshot.CapturedFile, treeHash string) artifact.Tree {
	entries := make([]artifact.Entry, 0, len(files))
	for _, f := range files {
		entries = append(entries, artifact.Entry{Path: f.Path, Kind: artifact.EntryFile, Mode: f.Mode, Bytes: append([]byte{}, f.Bytes...), Ownership: artifact.Ownership{EntryID: "snapshot.fork:" + f.Path, GroupID: "snapshot.fork"}, Provenance: artifact.Provenance{Source: "snapshot.fork", Revision: treeHash}})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return artifact.Tree{Entries: entries, Provenance: artifact.Provenance{Source: "snapshot.fork", Revision: treeHash}}
}
