package snapshot

import (
	"context"
	"strings"
)

func snapshotRefs(ctx context.Context, sg *ShadowGit, dir string, limit int) (map[string]string, error) {
	raw, err := boundedSnapshotGit(ctx, sg, dir, int64(limit)*1024, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, " ")
		if len(parts) != 2 || !validRetainedReference(parts[0]) || !validObjectID(parts[1]) || out[parts[0]] != "" || len(out) >= limit {
			return nil, ErrAdmissionUnavailable
		}
		out[parts[0]] = parts[1]
	}
	return out, nil
}

// captureReferences binds only refs newly created by this capture under the
// same admission lock. Reused/mutated refs are not silently adopted.
func captureReferences(ctx context.Context, sg *ShadowGit, id, commit string, before map[string]string, limit int) ([]string, error) {
	dir, err := sg.openShadowRepo(id)
	if err != nil {
		return nil, ErrAdmissionUnavailable
	}
	after, err := snapshotRefs(ctx, sg, dir, limit)
	if err != nil {
		return nil, err
	}
	var refs []string
	for ref, old := range before {
		if after[ref] != old {
			return nil, ErrAdmissionUnavailable
		}
	}
	for ref, oid := range after {
		if _, ok := before[ref]; !ok {
			if oid != commit {
				return nil, ErrAdmissionUnavailable
			}
			refs = append(refs, ref)
		}
	}
	if len(refs) != 1 {
		return nil, ErrAdmissionUnavailable
	}
	return refs, nil
}

// completeObjectOwnership refuses unreachable or foreign objects before GC.
// All current refs must already match exact persisted capture ownership.
func completeObjectOwnership(ctx context.Context, sg *ShadowGit, dir string, limit int) error {
	if _, err := boundedSnapshotGit(ctx, sg, dir, int64(limit)*128, "fsck", "--full", "--strict", "--no-reflogs"); err != nil {
		return err
	}
	reachable, err := boundedSnapshotGit(ctx, sg, dir, int64(limit)*4352, "rev-list", "--objects", "--all")
	if err != nil {
		return err
	}
	owned := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(reachable)), "\n") {
		if line == "" {
			continue
		}
		oid := strings.SplitN(line, " ", 2)[0]
		if !validObjectID(oid) || len(owned) >= limit {
			return ErrAdmissionUnavailable
		}
		owned[oid] = true
	}
	all, err := boundedSnapshotGit(ctx, sg, dir, int64(limit)*65, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	if err != nil {
		return err
	}
	count := 0
	for _, oid := range strings.Split(strings.TrimSpace(string(all)), "\n") {
		if oid == "" {
			continue
		}
		count++
		if count > limit || !owned[oid] {
			return ErrAdmissionUnavailable
		}
	}
	return nil
}
