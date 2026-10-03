package sqlitebackup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/go-sqlite/sqlitekit"
)

// existingTarget writes a distinct small database at a fresh path.
func existingTarget(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "live.db")
	db, err := sqlitekit.OpenSingle(context.Background(), path, sqlitekit.OpenOptions{
		Options: sqlitekit.Options{BusyTimeout: sqlitekit.DefaultBusyTimeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE old (id INTEGER PRIMARY KEY)`)
	mustExec(t, db, `INSERT INTO old DEFAULT VALUES`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, readFile(t, path)
}

func TestRestoreReplacesTargetAndKeepsOriginal(t *testing.T) {
	src, _ := newSourceDB(t)
	backup := makeBackup(t, src)
	target, original := existingTarget(t)

	res, err := Restore(context.Background(), target, backup, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.SupersededPath == "" {
		t.Fatal("no SupersededPath for a replaced file")
	}
	if !bytes.Equal(readFile(t, res.SupersededPath), original) {
		t.Fatal("superseded file is not the original target")
	}
	if !bytes.Equal(readFile(t, target), readFile(t, backup)) {
		t.Fatal("target is not byte-identical to the backup")
	}
	if got := count(t, openRO(t, target), "a"); got != 200 {
		t.Fatalf("restored rows = %d", got)
	}
	want := []string{"live.db", filepath.Base(res.SupersededPath)}
	slices.Sort(want)
	if names := listDir(t, filepath.Dir(target)); !slices.Equal(names, want) {
		t.Fatalf("directory = %v, want %v", names, want)
	}
}

func TestRestoreWithoutPriorTargetReportsNoSuperseded(t *testing.T) {
	src, _ := newSourceDB(t)
	backup := makeBackup(t, src)
	target := filepath.Join(t.TempDir(), "sub", "live.db")
	res, err := Restore(context.Background(), target, backup, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.SupersededPath != "" {
		t.Fatalf("SupersededPath = %q for a fresh target", res.SupersededPath)
	}
	if !bytes.Equal(readFile(t, target), readFile(t, backup)) {
		t.Fatal("target differs from backup")
	}
}

func TestRestoreRefusesDamagedSourceBeforeTouchingTarget(t *testing.T) {
	src, _ := newSourceDB(t)
	good := readFile(t, makeBackup(t, src))
	bad := slices.Clone(good)
	for i := 4096; i < len(bad); i++ {
		bad[i] = 0xFF
	}
	badPath := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(badPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	target, original := existingTarget(t)
	before := listDir(t, filepath.Dir(target))

	_, err := Restore(context.Background(), target, badPath, func(context.Context, *sql.DB) error {
		t.Error("postCheck ran against a refused source")
		return nil
	})
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("err = %v, want ErrIntegrity", err)
	}
	if !bytes.Equal(readFile(t, target), original) {
		t.Fatal("target modified by refused restore")
	}
	if after := listDir(t, filepath.Dir(target)); !slices.Equal(before, after) {
		t.Fatalf("directory changed: %v -> %v", before, after)
	}
}

func TestRestoreRefusesMissingAndSameFile(t *testing.T) {
	target, original := existingTarget(t)
	if _, err := Restore(context.Background(), target, filepath.Join(t.TempDir(), "none.db"), nil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing source: %v", err)
	}
	if _, err := Restore(context.Background(), target, target, nil); !errors.Is(err, ErrSameFile) {
		t.Errorf("same path: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link.db")
	if err := os.Link(target, link); err == nil {
		if _, err := Restore(context.Background(), target, link, nil); !errors.Is(err, ErrSameFile) {
			t.Errorf("hard-linked same file: %v", err)
		}
	}
	if !bytes.Equal(readFile(t, target), original) {
		t.Fatal("target modified")
	}
}

func TestRestorePostCheckSeesRestoredDataAndCanVeto(t *testing.T) {
	src, _ := newSourceDB(t)
	backup := makeBackup(t, src)

	t.Run("sees restored data", func(t *testing.T) {
		target, _ := existingTarget(t)
		var seen int
		_, err := Restore(context.Background(), target, backup, func(ctx context.Context, db *sql.DB) error {
			return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM a`).Scan(&seen)
		})
		if err != nil || seen != 200 {
			t.Fatalf("seen=%d err=%v", seen, err)
		}
	})

	t.Run("veto leaves target untouched", func(t *testing.T) {
		target, original := existingTarget(t)
		before := listDir(t, filepath.Dir(target))
		veto := errors.New("fingerprint mismatch")
		res, err := Restore(context.Background(), target, backup, func(context.Context, *sql.DB) error { return veto })
		if !errors.Is(err, veto) {
			t.Fatalf("err = %v, want veto", err)
		}
		if res.SupersededPath != "" {
			t.Fatalf("SupersededPath set on failure: %q", res.SupersededPath)
		}
		if !bytes.Equal(readFile(t, target), original) {
			t.Fatal("target changed after a vetoed restore")
		}
		if after := listDir(t, filepath.Dir(target)); !slices.Equal(before, after) {
			t.Fatalf("directory changed: %v -> %v", before, after)
		}
	})

	t.Run("veto on fresh target leaves nothing", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "live.db")
		_, err := Restore(context.Background(), target, backup, func(context.Context, *sql.DB) error { return errors.New("no") })
		if err == nil {
			t.Fatal("expected error")
		}
		if names := listDir(t, dir); len(names) != 0 {
			t.Fatalf("files left behind: %v", names)
		}
	})
}

func TestRestoreMovesWALCompanionsWithReplacedFile(t *testing.T) {
	src, _ := newSourceDB(t)
	backup := makeBackup(t, src)
	target, _ := existingTarget(t)
	if err := os.WriteFile(target+"-wal", []byte("stale wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+"-shm", []byte("stale shm"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(context.Background(), target, backup, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(target + s); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale %s left beside the restored database", s)
		}
		if !strings.HasPrefix(string(readFile(t, res.SupersededPath+s)), "stale") {
			t.Errorf("companion %s not preserved with the superseded file", s)
		}
	}
}

// TestRestoreAtomicityRollsBackOnFailureAfterMoveAside simulates a crash-like
// failure at the moment the original has been moved aside and the new file is
// not yet in place: everything must be put back exactly.
func TestRestoreAtomicityRollsBackOnFailureAfterMoveAside(t *testing.T) {
	src, _ := newSourceDB(t)
	backup := makeBackup(t, src)
	target, original := existingTarget(t)
	if err := os.WriteFile(target+"-wal", []byte("wal bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+"-shm", []byte("shm bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := listDir(t, filepath.Dir(target))

	boom := errors.New("simulated failure during swap")
	res, err := Restore(context.Background(), target, backup, nil, withHook(func(step, _ string) error {
		if step == "aside" {
			return boom
		}
		return nil
	}))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if res.SupersededPath != "" {
		t.Fatalf("SupersededPath set on failure: %q", res.SupersededPath)
	}
	if !bytes.Equal(readFile(t, target), original) {
		t.Fatal("original not restored after failed swap")
	}
	if string(readFile(t, target+"-wal")) != "wal bytes" || string(readFile(t, target+"-shm")) != "shm bytes" {
		t.Fatal("companions not put back")
	}
	if after := listDir(t, filepath.Dir(target)); !slices.Equal(before, after) {
		t.Fatalf("directory changed: %v -> %v", before, after)
	}
}

// The target is either the complete old file or the complete new file at every
// observable step; it is never absent or partial while the swap is prepared.
func TestRestoreTargetNeverPartialDuringStaging(t *testing.T) {
	src, _ := newSourceDB(t)
	backup := makeBackup(t, src)
	target, original := existingTarget(t)
	want := readFile(t, backup)
	_, err := Restore(context.Background(), target, backup, nil, withHook(func(step, _ string) error {
		if step == "staged" || step == "checked" {
			if !bytes.Equal(readFile(t, target), original) {
				t.Errorf("target changed before swap (step %s)", step)
			}
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, target), want) {
		t.Fatal("target is not the full backup")
	}
}

func TestRestoreCopyGuardRejectsDigestMismatch(t *testing.T) {
	src, _ := newSourceDB(t)
	backup := makeBackup(t, src)
	stage := filepath.Join(t.TempDir(), "stage")
	if err := os.WriteFile(stage, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyVerified(backup, stage, strings.Repeat("0", 64), 0o600); err == nil {
		t.Fatal("copyVerified accepted a digest mismatch")
	}
}
