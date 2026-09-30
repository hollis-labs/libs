package sqlitebackup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/go-sqlite/sqlitekit"
)

func TestBackupOnlineProducesVerifiedCopy(t *testing.T) {
	db, _ := newSourceDB(t)
	dest := filepath.Join(t.TempDir(), "out", "backup.db")

	res, err := Backup(context.Background(), db, dest)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if res.Mode != Online || !res.IntegrityOK || res.Path != dest || len(res.Damage) != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.TakenAt.IsZero() || res.SizeBytes <= 0 || len(res.SHA256) != 64 {
		t.Fatalf("result missing facts: %+v", res)
	}
	copyDB := openRO(t, dest)
	if got := count(t, copyDB, "a"); got != 200 {
		t.Fatalf("rows in copy = %d, want 200", got)
	}
	if got := count(t, copyDB, "b"); got != 200 {
		t.Fatalf("rows in copy b = %d, want 200", got)
	}
}

func TestBackupWritesMinimalManifest(t *testing.T) {
	db, _ := newSourceDB(t)
	dest := filepath.Join(t.TempDir(), "backup.db")
	res, err := Backup(context.Background(), db, dest)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(readFile(t, dest+".manifest.json"), &m); err != nil {
		t.Fatalf("manifest json: %v", err)
	}
	if m.SHA256 != res.SHA256 || m.SizeBytes != res.SizeBytes || m.Mode != Online || !m.IntegrityOK || !m.TakenAt.Equal(res.TakenAt) {
		t.Fatalf("manifest %+v does not match result %+v", m, res)
	}
	var raw map[string]any
	if err := json.Unmarshal(readFile(t, dest+".manifest.json"), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 5 {
		t.Fatalf("manifest carries fields beyond the mechanical five: %v", raw)
	}
}

func TestBackupCustomAndDisabledManifest(t *testing.T) {
	db, _ := newSourceDB(t)
	dir := t.TempDir()
	custom := filepath.Join(dir, "meta", "m.json")
	if _, err := Backup(context.Background(), db, filepath.Join(dir, "a.db"), WithManifest(custom)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatalf("custom manifest missing: %v", err)
	}
	if _, err := Backup(context.Background(), db, filepath.Join(dir, "b.db"), WithManifest("")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.db.manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest written despite WithManifest(\"\"): %v", err)
	}
}

func TestBackupResultChecksumMatchesFileOnDisk(t *testing.T) {
	db, _ := newSourceDB(t)
	dest := filepath.Join(t.TempDir(), "backup.db")
	res, err := Backup(context.Background(), db, dest)
	if err != nil {
		t.Fatal(err)
	}
	size, sum, err := statDigest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA256 != sum || res.SizeBytes != size {
		t.Fatalf("result says %s/%d, disk says %s/%d", res.SHA256, res.SizeBytes, sum, size)
	}
	v, err := Verify(context.Background(), dest)
	if err != nil || v.SHA256 != res.SHA256 {
		t.Fatalf("Verify disagrees with Backup: %+v %v", v, err)
	}
}

// TestBackupChecksumAfterVerification pins the ordering from Tangent's
// CW-20260905-0014: the checksum must be taken after integrity verification,
// because verification can rewrite the file. The hook simulates that rewrite
// (the WAL flag bytes in the header) while verification is running. If Backup
// took the checksum first, the result would describe a file that is gone.
func TestBackupChecksumAfterVerification(t *testing.T) {
	db, _ := newSourceDB(t)
	dest := filepath.Join(t.TempDir(), "backup.db")

	var steps []string
	var prePublish []byte
	hook := func(step, path string) error {
		steps = append(steps, step)
		switch step {
		case "verifying":
			// Verification rewrites the header, as Tangent's opener did.
			f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // test path
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if _, err := f.WriteAt([]byte{2, 2}, 18); err != nil {
				return err
			}
		case "prepublish":
			prePublish = readFile(t, path)
		}
		return nil
	}
	res, err := Backup(context.Background(), db, dest, withHook(hook))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"vacuumed", "verifying", "verified", "checksummed", "prepublish"}
	if !slices.Equal(steps, want) {
		t.Fatalf("step order = %v, want %v", steps, want)
	}
	final := readFile(t, dest)
	if !bytes.Equal(final, prePublish) || final[18] != 2 || final[19] != 2 {
		t.Fatal("test setup: verification's rewrite did not reach the published file")
	}
	_, sum, err := statDigest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA256 != sum {
		t.Fatalf("checksum taken before verification: result %s, file %s", res.SHA256, sum)
	}
	var m Manifest
	if err := json.Unmarshal(readFile(t, dest+".manifest.json"), &m); err != nil || m.SHA256 != sum {
		t.Fatalf("manifest checksum %q does not match file %q (%v)", m.SHA256, sum, err)
	}
}

func TestBackupWithVerifyFalseSkipsCheckButStillChecksums(t *testing.T) {
	db, _ := newSourceDB(t)
	dest := filepath.Join(t.TempDir(), "backup.db")
	var steps []string
	res, err := Backup(context.Background(), db, dest, WithVerify(false),
		withHook(func(step, _ string) error { steps = append(steps, step); return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(steps, "verifying") {
		t.Fatalf("verification ran despite WithVerify(false): %v", steps)
	}
	if res.IntegrityOK {
		t.Fatal("IntegrityOK must be false when verification was skipped")
	}
	if _, sum, _ := statDigest(dest); sum != res.SHA256 {
		t.Fatal("checksum mismatch")
	}
}

func TestBackupDrainedMode(t *testing.T) {
	db, src := newSourceDB(t)
	dest := filepath.Join(t.TempDir(), "backup.db")
	res, err := Backup(context.Background(), db, dest, WithMode(Drained))
	if err != nil {
		t.Fatalf("Backup drained: %v", err)
	}
	if res.Mode != Drained || !res.IntegrityOK {
		t.Fatalf("unexpected result: %+v", res)
	}
	if fi, err := os.Stat(src + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("WAL not truncated by drained backup: %d bytes", fi.Size())
	}
	if got := count(t, openRO(t, dest), "a"); got != 200 {
		t.Fatalf("rows = %d", got)
	}
}

func TestBackupDrainedRefusesWhenBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "src.db")
	opts := sqlitekit.OpenOptions{
		Options:      sqlitekit.Options{WAL: true, BusyTimeout: 50 * time.Millisecond},
		MaxOpenConns: 2,
	}
	db, err := sqlitekit.OpenReader(context.Background(), path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `CREATE TABLE a (id INTEGER PRIMARY KEY, v TEXT)`)
	mustExec(t, db, `INSERT INTO a(v) VALUES ('x')`)

	// Another connection holds a read transaction open, so TRUNCATE cannot
	// complete.
	ctx := context.Background()
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	tx, err := holder.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if scanErr := tx.QueryRow(`SELECT COUNT(*) FROM a`).Scan(&n); scanErr != nil {
		t.Fatal(scanErr)
	}
	mustExec(t, db, `INSERT INTO a(v) VALUES ('y')`) // put frames in the WAL

	dest := filepath.Join(t.TempDir(), "backup.db")
	_, err = Backup(ctx, db, dest, WithMode(Drained))
	if !errors.Is(err, ErrDrainBusy) {
		t.Fatalf("err = %v, want ErrDrainBusy", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination exists after refused drain: %v", statErr)
	}
	if left := listDir(t, filepath.Dir(dest)); len(left) != 0 {
		t.Fatalf("files left behind: %v", left)
	}
}

func TestBackupRefusesExistingDestinationWithoutClobbering(t *testing.T) {
	db, _ := newSourceDB(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.db")
	precious := []byte("do not overwrite me")
	if err := os.WriteFile(dest, precious, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Backup(context.Background(), db, dest)
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("err = %v, want ErrDestinationExists", err)
	}
	if !bytes.Equal(readFile(t, dest), precious) {
		t.Fatal("existing destination was modified")
	}
	if names := listDir(t, dir); !slices.Equal(names, []string{"backup.db"}) {
		t.Fatalf("stray files after refusal: %v", names)
	}
}

// A destination that appears while the backup is being prepared must still not
// be overwritten: publication is a no-clobber link, not a blind rename.
func TestBackupDestinationAppearingMidFlightIsNotClobbered(t *testing.T) {
	db, _ := newSourceDB(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.db")
	precious := []byte("appeared late")
	_, err := Backup(context.Background(), db, dest, withHook(func(step, _ string) error {
		if step == "prepublish" {
			return os.WriteFile(dest, precious, 0o600)
		}
		return nil
	}))
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("err = %v, want ErrDestinationExists", err)
	}
	if !bytes.Equal(readFile(t, dest), precious) {
		t.Fatal("late destination was clobbered")
	}
	if names := listDir(t, dir); !slices.Equal(names, []string{"backup.db"}) {
		t.Fatalf("stray files: %v", names)
	}
}

// TestBackupFailureLeavesNoPartialFile covers every point at which a backup
// can fail: the destination directory must end up holding nothing new.
func TestBackupFailureLeavesNoPartialFile(t *testing.T) {
	boom := errors.New("simulated crash")
	corrupt := func(step, path string) error {
		if step != "vacuumed" {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // test path
		if err != nil {
			return err
		}
		for i := 4096; i < len(b); i++ {
			b[i] = 0xFF
		}
		return os.WriteFile(path, b, 0o600) //nolint:gosec // test path
	}
	failAt := func(want string) func(string, string) error {
		return func(step, _ string) error {
			if step == want {
				return boom
			}
			return nil
		}
	}
	cases := []struct {
		name string
		hook func(string, string) error
		is   error
	}{
		{"after vacuum", failAt("vacuumed"), boom},
		{"after verify", failAt("verified"), boom},
		{"after checksum", failAt("checksummed"), boom},
		{"before publish", failAt("prepublish"), boom},
		{"corrupt snapshot", corrupt, ErrIntegrity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := newSourceDB(t)
			dir := t.TempDir()
			dest := filepath.Join(dir, "backup.db")
			res, err := Backup(context.Background(), db, dest, withHook(tc.hook))
			if !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want %v", err, tc.is)
			}
			if errors.Is(tc.is, ErrIntegrity) && (res.IntegrityOK || len(res.Damage) == 0) {
				t.Fatalf("integrity failure not described: %+v", res)
			}
			if names := listDir(t, dir); len(names) != 0 {
				t.Fatalf("failed backup left files behind: %v", names)
			}
		})
	}

	t.Run("canceled context", func(t *testing.T) {
		db, _ := newSourceDB(t)
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Backup(ctx, db, filepath.Join(dir, "backup.db")); err == nil {
			t.Fatal("expected error from canceled context")
		}
		if names := listDir(t, dir); len(names) != 0 {
			t.Fatalf("files left behind: %v", names)
		}
	})
	t.Run("closed database", func(t *testing.T) {
		db, _ := newSourceDB(t)
		_ = db.Close()
		dir := t.TempDir()
		if _, err := Backup(context.Background(), db, filepath.Join(dir, "backup.db")); err == nil {
			t.Fatal("expected error from closed database")
		}
		if names := listDir(t, dir); len(names) != 0 {
			t.Fatalf("files left behind: %v", names)
		}
	})
}

func TestBackupManifestPublishFailureRemovesDatabase(t *testing.T) {
	db, _ := newSourceDB(t)
	dir := t.TempDir()
	// A directory sits where the manifest must go, so the final rename fails
	// after the database has been published.
	blocker := filepath.Join(dir, "m.json")
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o750); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "backup.db")
	if _, err := Backup(context.Background(), db, dest, WithManifest(blocker)); err == nil {
		t.Fatal("expected manifest failure")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database left published without its manifest: %v", err)
	}
	if names := listDir(t, dir); !slices.Equal(names, []string{"m.json"}) {
		t.Fatalf("stray files: %v", names)
	}
}

func TestBackupRejectsBadArguments(t *testing.T) {
	db, _ := newSourceDB(t)
	ctx := context.Background()
	if _, err := Backup(ctx, nil, filepath.Join(t.TempDir(), "x.db")); err == nil {
		t.Error("nil db accepted")
	}
	if _, err := Backup(ctx, db, "  "); err == nil {
		t.Error("blank destination accepted")
	}
	if _, err := Backup(ctx, db, filepath.Join(t.TempDir(), "x.db"), WithMode("weird")); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestBackupInMemorySource(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	mustExec(t, db, `CREATE TABLE a (id INTEGER PRIMARY KEY, v TEXT)`)
	mustExec(t, db, `INSERT INTO a(v) VALUES ('hello')`)
	for _, mode := range []Mode{Online, Drained} {
		dest := filepath.Join(t.TempDir(), "mem.db")
		res, err := Backup(context.Background(), db, dest, WithMode(mode))
		if err != nil || !res.IntegrityOK {
			t.Fatalf("%s: %+v %v", mode, res, err)
		}
		if got := count(t, openRO(t, dest), "a"); got != 1 {
			t.Fatalf("%s: rows = %d", mode, got)
		}
	}
}

// TestBackupWhileConcurrentWriterIsSnapshotConsistent backs up repeatedly
// while another goroutine commits paired inserts. Every snapshot must pass
// integrity_check and must never show a half-applied pair.
func TestBackupWhileConcurrentWriterIsSnapshotConsistent(t *testing.T) {
	db, _ := newSourceDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var written atomic.Int64
	var writeErr atomic.Value
	wg.Go(func() {
		for i := 1000; ctx.Err() == nil; i++ {
			if err := insertPair(context.Background(), db, i); err != nil {
				writeErr.Store(err)
				return
			}
			written.Add(1)
		}
	})
	defer func() {
		cancel()
		wg.Wait()
		if err, _ := writeErr.Load().(error); err != nil {
			t.Errorf("concurrent writer failed: %v", err)
		}
	}()

	dir := t.TempDir()
	prev := 0
	for i := 0; i < 5; i++ {
		before := written.Load()
		dest := filepath.Join(dir, "b"+string(rune('0'+i))+".db")
		res, err := Backup(context.Background(), db, dest)
		if err != nil {
			t.Fatalf("backup %d under write load: %v", i, err)
		}
		if !res.IntegrityOK {
			t.Fatalf("backup %d not intact: %+v", i, res)
		}
		snap := openRO(t, dest)
		a, b := count(t, snap, "a"), count(t, snap, "b")
		if a != b {
			t.Fatalf("torn snapshot: a=%d b=%d", a, b)
		}
		if int64(a) < 200+before {
			t.Fatalf("snapshot lost writes committed before it began: rows=%d, committed before=%d", a, 200+before)
		}
		if a < prev {
			t.Fatalf("later snapshot has fewer rows (%d < %d)", a, prev)
		}
		prev = a
		v, err := Verify(context.Background(), dest)
		if err != nil || v.SHA256 != res.SHA256 {
			t.Fatalf("Verify after backup: %+v %v", v, err)
		}
	}
}
