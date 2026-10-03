package sqlitebackup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hollis-labs/libs/util/sqlite/sqlitekit"
)

// RestoreResult is what Restore did.
type RestoreResult struct {
	// SupersededPath is where the file Restore replaced was kept. It is never
	// deleted by this package, and is empty if targetPath did not exist or the
	// restore failed. The replaced file's -wal/-shm companions, if any, sit
	// beside it with the same suffixes.
	SupersededPath string `json:"superseded_path,omitempty"`
}

// Restore replaces the database at targetPath with the backup at sourcePath.
//
// It verifies sourcePath first (always; WithVerify does not apply here) and
// refuses a damaged backup before touching anything. It then copies the
// backup to a staging file beside targetPath, checks the copy's digest against
// the verified source, and runs postCheck (when non-nil) against the staged
// copy, opened read-only. Only then does it swap: the file being replaced is
// preserved at SupersededPath (never deleted), its -wal/-shm companions are
// moved with it so they cannot resurrect old transactions in the new
// database, and the staged file is renamed over targetPath in one atomic
// step. If anything before or during the swap fails, the original file and
// its companions are put back and the staging file is removed. If postCheck
// fails, targetPath was never touched.
//
// The caller must already hold whatever lock or drain guarantee makes
// replacing targetPath safe; Restore does not take one. Stray -wal/-shm files
// beside sourcePath are ignored: only the main file is restored.
//
// postCheck is the seam for an application's own preservation check (row
// counts, a content fingerprint); this package knows nothing about schemas.
func Restore(
	ctx context.Context,
	targetPath, sourcePath string,
	postCheck func(context.Context, *sql.DB) error,
	opts ...Option,
) (RestoreResult, error) {
	o := newOptions(opts)
	target, err := resolve(targetPath)
	if err != nil {
		return RestoreResult{}, err
	}
	source, err := resolve(sourcePath)
	if err != nil {
		return RestoreResult{}, err
	}
	if source == target {
		return RestoreResult{}, ErrSameFile
	}
	oldInfo, statErr := os.Lstat(target)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return RestoreResult{}, fmt.Errorf("sqlitebackup: stat target: %w", statErr)
	}
	if existed {
		if !oldInfo.Mode().IsRegular() {
			return RestoreResult{}, fmt.Errorf("sqlitebackup: target %q is not a regular file", target)
		}
		if srcInfo, sErr := os.Stat(source); sErr == nil && os.SameFile(srcInfo, oldInfo) {
			return RestoreResult{}, ErrSameFile
		}
	}

	verified, err := Verify(ctx, source, opts...)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("sqlitebackup: refusing to restore from %q: %w", source, err)
	}

	dir := filepath.Dir(target)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return RestoreResult{}, fmt.Errorf("sqlitebackup: create target directory: %w", mkErr)
	}
	stage, err := reserveTemp(dir, filepath.Base(target))
	if err != nil {
		return RestoreResult{}, err
	}
	defer removeAll(stage, stage+"-wal", stage+"-shm")

	mode := os.FileMode(0o600)
	if existed {
		mode = oldInfo.Mode().Perm()
	}
	if err = copyVerified(source, stage, verified.SHA256, mode); err != nil {
		return RestoreResult{}, err
	}
	if hErr := o.step("staged", stage); hErr != nil {
		return RestoreResult{}, hErr
	}

	if postCheck != nil {
		if err = runPostCheck(ctx, stage, &o, postCheck); err != nil {
			return RestoreResult{}, err
		}
	}
	if hErr := o.step("checked", stage); hErr != nil {
		return RestoreResult{}, hErr
	}

	res, err := swap(target, stage, existed, &o)
	if err != nil {
		return RestoreResult{}, err
	}
	syncDir(dir)
	return res, nil
}

func runPostCheck(ctx context.Context, stage string, o *options, postCheck func(context.Context, *sql.DB) error) error {
	cleanup := noteSidecars(stage)
	defer cleanup()
	db, err := sqlitekit.OpenReadOnly(ctx, stage, sqlitekit.OpenOptions{
		Options:      sqlitekit.Options{BusyTimeout: sqlitekit.DefaultBusyTimeout},
		DriverName:   o.driver,
		MaxOpenConns: 1,
	})
	if err != nil {
		return fmt.Errorf("sqlitebackup: open restored copy: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := postCheck(ctx, db); err != nil {
		return fmt.Errorf("sqlitebackup: post-restore check failed, target left untouched: %w", err)
	}
	return nil
}

// swap preserves the current target (if any) and renames stage over it,
// rolling back on failure.
func swap(target, stage string, existed bool, o *options) (RestoreResult, error) {
	var (
		res          RestoreResult
		superseded   string
		movedByRen   bool
		movedCompany []string
	)
	rollback := func() {
		for _, s := range movedCompany {
			_ = os.Rename(superseded+s, target+s)
		}
		if existed {
			if movedByRen {
				_ = os.Rename(superseded, target)
			} else {
				_ = os.Remove(superseded)
			}
		}
	}

	if existed {
		var err error
		superseded, err = supersededName(target)
		if err != nil {
			return res, err
		}
		// A hard link keeps the old file reachable while target stays put
		// until the final atomic rename; fall back to a rename where links
		// are unsupported.
		if lnErr := os.Link(target, superseded); lnErr != nil {
			if rnErr := os.Rename(target, superseded); rnErr != nil {
				return res, fmt.Errorf("sqlitebackup: move existing database aside: %w", rnErr)
			}
			movedByRen = true
		}
		// The companions belong to the file being replaced. Left beside the
		// restored database they would replay its old transactions.
		for _, s := range sidecarSuffixes {
			if _, err := os.Lstat(target + s); err != nil {
				continue
			}
			if rnErr := os.Rename(target+s, superseded+s); rnErr != nil {
				rollback()
				return res, fmt.Errorf("sqlitebackup: move %s companion aside: %w", s, rnErr)
			}
			movedCompany = append(movedCompany, s)
		}
	}
	if hErr := o.step("aside", target); hErr != nil {
		rollback()
		return res, hErr
	}
	if rnErr := os.Rename(stage, target); rnErr != nil {
		rollback()
		return res, fmt.Errorf("sqlitebackup: move restored database into place: %w", rnErr)
	}
	res.SupersededPath = superseded
	return res, nil
}

func supersededName(target string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for i := 0; i < 1000; i++ {
		name := target + ".superseded-" + stamp
		if i > 0 {
			name = fmt.Sprintf("%s-%d", name, i)
		}
		free := true
		for _, s := range append([]string{""}, sidecarSuffixes...) {
			if _, err := os.Lstat(name + s); err == nil {
				free = false
			}
		}
		if free {
			return name, nil
		}
	}
	return "", errors.New("sqlitebackup: no free superseded name")
}

// copyVerified copies src to dst, syncs it, and fails unless the bytes written
// hash to want (the digest Verify took of src).
func copyVerified(src, dst, want string, mode os.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // caller-named path
	if err != nil {
		return fmt.Errorf("sqlitebackup: open backup: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // our own temp file
	if err != nil {
		return fmt.Errorf("sqlitebackup: open staging file: %w", err)
	}
	h := sha256.New()
	_, cpErr := io.Copy(io.MultiWriter(out, h), in)
	syncErr := out.Sync()
	if err := errors.Join(cpErr, syncErr, out.Close()); err != nil {
		return fmt.Errorf("sqlitebackup: copy backup into staging: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("sqlitebackup: backup changed while being copied (digest %s, verified %s)", got, want)
	}
	if err := os.Chmod(dst, mode); err != nil {
		return fmt.Errorf("sqlitebackup: chmod staging file: %w", err)
	}
	return nil
}
