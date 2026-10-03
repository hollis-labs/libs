package sqlitebackup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Mode distinguishes the two backup guarantees a caller can ask for.
type Mode string

const (
	// Online runs against a database other writers may still be using.
	// VACUUM INTO executes inside a read transaction, so the destination is a
	// complete, consistent snapshot of everything committed before the read
	// transaction began, never a torn one, but a write committed while the
	// copy was in flight is not guaranteed to be in it.
	Online Mode = "online"

	// Drained checkpoints the WAL (PRAGMA wal_checkpoint(TRUNCATE)) before
	// the snapshot, folding every committed write into the main file and
	// leaving an empty WAL. TRUNCATE blocks on other readers, so a caller
	// passing Drained is asserting it already holds whatever lock makes that
	// safe: this package takes no lock on the caller's behalf. If the
	// checkpoint cannot complete, Backup fails with ErrDrainBusy.
	Drained Mode = "drained"
)

// Sentinel errors. Match them with errors.Is.
var (
	// ErrDestinationExists is returned by Backup when the destination path is
	// already occupied. Backup never overwrites.
	ErrDestinationExists = errors.New("sqlitebackup: destination already exists")

	// ErrIntegrity is returned (wrapped) when a database file fails
	// verification: bad header, empty file, or PRAGMA integrity_check
	// reporting anything other than "ok".
	ErrIntegrity = errors.New("sqlitebackup: integrity check failed")

	// ErrDrainBusy is returned by Backup in Drained mode when the WAL
	// checkpoint could not complete because another connection holds the
	// database.
	ErrDrainBusy = errors.New("sqlitebackup: wal checkpoint did not complete")

	// ErrSameFile is returned by Restore when source and target are the same
	// file.
	ErrSameFile = errors.New("sqlitebackup: source and target are the same file")
)

const (
	defaultDriverName = "sqlite"
	manifestSuffix    = ".manifest.json"
)

// Result is what Backup or Verify produced.
type Result struct {
	// Path is the absolute path of the database file described.
	Path string `json:"path"`
	// Mode is the backup mode. Empty for Verify.
	Mode Mode `json:"mode,omitempty"`
	// TakenAt is when the backup began. Zero for Verify.
	TakenAt time.Time `json:"taken_at,omitzero"`
	// SizeBytes is the size of the file described.
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 is the hex digest of the file as it exists on disk after
	// verification. See the ordering note on Backup.
	SHA256 string `json:"sha256"`
	// IntegrityOK reports that the file passed verification. It is false when
	// verification failed and also when Backup ran WithVerify(false).
	IntegrityOK bool `json:"integrity_ok"`
	// Damage holds the reasons verification failed: PRAGMA integrity_check's
	// non-"ok" rows verbatim, or a description of a header/read failure.
	// Empty when IntegrityOK is true or verification was skipped.
	Damage []string `json:"damage,omitempty"`
}

// Manifest is the minimal, mechanical sidecar Backup writes. It carries
// nothing domain-specific: no schema version, no content fingerprint, no
// table list. An adopter that wants domain fields keeps writing its own
// sidecar alongside this one.
type Manifest struct {
	Mode        Mode      `json:"mode"`
	TakenAt     time.Time `json:"taken_at"`
	SizeBytes   int64     `json:"size_bytes"`
	SHA256      string    `json:"sha256"`
	IntegrityOK bool      `json:"integrity_ok"`
}

// Option configures Backup, Verify, and Restore.
type Option func(*options)

type options struct {
	mode        Mode
	verify      bool
	driver      string
	manifest    string
	manifestSet bool

	// hook is a test seam: it is called at named steps and may fail the
	// operation or mutate the file at path. Never set outside tests.
	hook func(step, path string) error
}

func newOptions(opts []Option) options {
	o := options{mode: Online, verify: true, driver: defaultDriverName}
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

func (o *options) step(name, path string) error {
	if o.hook == nil {
		return nil
	}
	return o.hook(name, path)
}

// WithMode selects Online (default) or Drained. Backup only.
func WithMode(m Mode) Option { return func(o *options) { o.mode = m } }

// WithVerify toggles Backup's post-write verification. Default true.
// Verification cannot be disabled for Verify itself or for the source check
// Restore performs: those exist to verify.
func WithVerify(verify bool) Option { return func(o *options) { o.verify = verify } }

// WithDriverName sets the database/sql driver name used to reopen a file for
// verification. Default "sqlite" (modernc.org/sqlite's registered name). This
// package never imports a driver itself; the caller must have registered one.
func WithDriverName(name string) Option { return func(o *options) { o.driver = name } }

// WithManifest sets where Backup writes its sidecar Manifest as JSON.
// Default destPath+".manifest.json". Pass "" to skip writing a manifest.
func WithManifest(path string) Option {
	return func(o *options) { o.manifest, o.manifestSet = path, true }
}

// Backup writes a consistent point-in-time copy of db to destPath and returns
// a Result describing it.
//
// The steps, in this order, are load-bearing:
//
//  1. VACUUM INTO a temporary file beside destPath (after a WAL checkpoint in
//     Drained mode). The live database file is never copied raw.
//  2. Reopen the temporary file read-only and run PRAGMA integrity_check
//     (skipped by WithVerify(false)).
//  3. Take the SHA-256 checksum and size. This MUST come after step 2, never
//     before: opening a VACUUM INTO output to verify it can rewrite its
//     header (Tangent hit exactly this as CW-20260905-0014, when its
//     verifier applied PRAGMA journal_mode=WAL), so a checksum taken first
//     describes a file that no longer exists. This package's own read-only
//     opener does not rewrite the header today; the order is kept anyway
//     because a driver or option change should not be able to reintroduce the
//     bug. Do not "simplify" it.
//  4. Atomically publish: the finished temporary file is hard-linked to
//     destPath (which fails if destPath appeared meanwhile) and the
//     temporary name removed, so destPath is either absent or complete.
//
// On any failure the temporary file is removed and destPath is left exactly as
// it was; an existing destPath is never overwritten and fails with
// ErrDestinationExists. Temporary files are named ".<base>.tmp-*" beside
// destPath; only a hard process kill can leave one behind.
//
// A verification failure returns the Result (IntegrityOK false, Damage set)
// together with an error wrapping ErrIntegrity, and no file is published.
//
// The manifest is published after the database; if writing it fails the
// database is removed again and Backup returns the error.
func Backup(ctx context.Context, db *sql.DB, destPath string, opts ...Option) (res Result, err error) {
	o := newOptions(opts)
	if db == nil {
		return Result{}, errors.New("sqlitebackup: nil database")
	}
	if strings.TrimSpace(destPath) == "" {
		return Result{}, errors.New("sqlitebackup: no destination path")
	}
	if o.mode != Online && o.mode != Drained {
		return Result{}, fmt.Errorf("sqlitebackup: unknown mode %q", o.mode)
	}
	dest, err := filepath.Abs(destPath)
	if err != nil {
		return Result{}, fmt.Errorf("sqlitebackup: resolve destination: %w", err)
	}
	if _, statErr := os.Lstat(dest); statErr == nil {
		return Result{}, fmt.Errorf("%w: %q", ErrDestinationExists, dest)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, fmt.Errorf("sqlitebackup: stat destination: %w", statErr)
	}
	manifestPath := dest + manifestSuffix
	if o.manifestSet {
		manifestPath = o.manifest
	}
	if mkErr := os.MkdirAll(filepath.Dir(dest), 0o750); mkErr != nil {
		return Result{}, fmt.Errorf("sqlitebackup: create destination directory: %w", mkErr)
	}

	tmp, err := reserveTemp(filepath.Dir(dest), filepath.Base(dest))
	if err != nil {
		return Result{}, err
	}
	// VACUUM INTO refuses an existing file, so release the reserved name and
	// let SQLite create it.
	if rmErr := os.Remove(tmp); rmErr != nil {
		return Result{}, fmt.Errorf("sqlitebackup: release temp name: %w", rmErr)
	}
	defer func() {
		// Success renames/links tmp away, making these no-ops. Failure must
		// leave nothing behind.
		removeAll(tmp, tmp+"-wal", tmp+"-shm", tmp+"-journal")
	}()

	if o.mode == Drained {
		if cpErr := checkpointTruncate(ctx, db); cpErr != nil {
			return Result{}, cpErr
		}
	}

	takenAt := time.Now().UTC()
	if _, vErr := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); vErr != nil {
		return Result{}, fmt.Errorf("sqlitebackup: vacuum into temp file: %w", vErr)
	}
	if hErr := o.step("vacuumed", tmp); hErr != nil {
		return Result{}, hErr
	}

	res = Result{Path: dest, Mode: o.mode, TakenAt: takenAt}
	if o.verify {
		damage, vErr := checkIntegrity(ctx, tmp, &o)
		if vErr != nil {
			return res, vErr
		}
		if len(damage) > 0 {
			res.Damage = damage
			return res, fmt.Errorf("%w: backup of %q: %s", ErrIntegrity, dest, strings.Join(damage, "; "))
		}
		res.IntegrityOK = true
	}
	if hErr := o.step("verified", tmp); hErr != nil {
		return res, hErr
	}

	// Checksum strictly after verification. See step 3 in the doc comment.
	res.SizeBytes, res.SHA256, err = statDigest(tmp)
	if err != nil {
		return res, err
	}
	if o.hook != nil {
		if hErr := o.step("checksummed", tmp); hErr != nil {
			return res, hErr
		}
	}
	if chErr := os.Chmod(tmp, 0o600); chErr != nil {
		return res, fmt.Errorf("sqlitebackup: chmod backup: %w", chErr)
	}
	if syErr := syncFile(tmp); syErr != nil {
		return res, syErr
	}

	var manifestTmp string
	if manifestPath != "" {
		manifestTmp, err = writeManifestTemp(manifestPath, Manifest{
			Mode: res.Mode, TakenAt: res.TakenAt, SizeBytes: res.SizeBytes,
			SHA256: res.SHA256, IntegrityOK: res.IntegrityOK,
		})
		if err != nil {
			return res, err
		}
		defer removeAll(manifestTmp)
	}
	if hErr := o.step("prepublish", tmp); hErr != nil {
		return res, hErr
	}

	if pubErr := publishNoClobber(tmp, dest); pubErr != nil {
		return res, pubErr
	}
	if manifestPath != "" {
		if rnErr := os.Rename(manifestTmp, manifestPath); rnErr != nil {
			// Do not leave a database whose sidecar the caller was told
			// exists. Removing dest is safe: we published it a moment ago.
			removeAll(dest)
			return res, fmt.Errorf("sqlitebackup: publish manifest: %w", rnErr)
		}
	}
	syncDir(filepath.Dir(dest))
	return res, nil
}

func writeManifestTemp(manifestPath string, m Manifest) (string, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("sqlitebackup: encode manifest: %w", err)
	}
	dir := filepath.Dir(manifestPath)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return "", fmt.Errorf("sqlitebackup: create manifest directory: %w", mkErr)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(manifestPath)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("sqlitebackup: create manifest temp: %w", err)
	}
	name := f.Name()
	_, wErr := f.Write(append(data, '\n'))
	cErr := errors.Join(f.Sync(), f.Close())
	if err := errors.Join(wErr, cErr, os.Chmod(name, 0o600)); err != nil {
		removeAll(name)
		return "", fmt.Errorf("sqlitebackup: write manifest: %w", err)
	}
	return name, nil
}

// checkpointTruncate folds the WAL into the main file and truncates it.
func checkpointTruncate(ctx context.Context, db *sql.DB) error {
	var busy, logFrames, checkpointed int
	err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // a database with no WAL may answer with no row
		}
		return fmt.Errorf("%w: %w", ErrDrainBusy, err)
	}
	if busy != 0 {
		return fmt.Errorf("%w: another connection holds the database", ErrDrainBusy)
	}
	return nil
}
