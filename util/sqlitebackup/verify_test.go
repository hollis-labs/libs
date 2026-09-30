package sqlitebackup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestVerifyAcceptsGoodBackup(t *testing.T) {
	db, _ := newSourceDB(t)
	path := makeBackup(t, db)
	res, err := Verify(context.Background(), path)
	if err != nil || !res.IntegrityOK || len(res.Damage) != 0 || res.SHA256 == "" || res.SizeBytes == 0 {
		t.Fatalf("good backup rejected: %+v %v", res, err)
	}
}

// TestVerifyDetectsCorruption: a damaged file is never reported ok, by either
// the Result or the error.
func TestVerifyDetectsCorruption(t *testing.T) {
	db, _ := newSourceDB(t)
	good := readFile(t, makeBackup(t, db))
	if len(good) < 3*4096 {
		t.Fatalf("test database too small (%d bytes) to damage", len(good))
	}
	mutate := map[string]func([]byte) []byte{
		"empty":            func([]byte) []byte { return nil },
		"tiny":             func(b []byte) []byte { return b[:100] },
		"garbage":          func(b []byte) []byte { return bytes.Repeat([]byte{0xAB}, len(b)) },
		"header clobbered": func(b []byte) []byte { c := slices.Clone(b); copy(c, "NOT A DATABASE!!"); return c },
		"data pages smashed": func(b []byte) []byte {
			c := slices.Clone(b)
			for i := 4096; i < len(c); i++ {
				c[i] = 0xFF
			}
			return c
		},
		"one page smashed": func(b []byte) []byte {
			c := slices.Clone(b)
			for i := 2 * 4096; i < 3*4096; i++ {
				c[i] = 0x00
			}
			return c
		},
		"tail truncated": func(b []byte) []byte { return b[:len(b)-2*4096] },
	}
	for name, fn := range mutate {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.db")
			if err := os.WriteFile(path, fn(good), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := Verify(context.Background(), path)
			if res.IntegrityOK {
				t.Fatalf("corrupt file reported ok: %+v", res)
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("err = %v, want ErrIntegrity", err)
			}
			if len(res.Damage) == 0 {
				t.Fatal("no damage described")
			}
		})
	}
}

func TestVerifyUnexaminableFilesAreErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Verify(context.Background(), filepath.Join(dir, "missing.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: err = %v", err)
	}
	if _, err := Verify(context.Background(), dir); err == nil {
		t.Error("directory accepted")
	}
	if names := listDir(t, dir); len(names) != 0 {
		t.Errorf("Verify created files: %v", names)
	}
}

func TestVerifyDoesNotModifyTheFile(t *testing.T) {
	db, _ := newSourceDB(t)
	path := makeBackup(t, db)
	before := readFile(t, path)
	if _, err := Verify(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, readFile(t, path)) {
		t.Fatal("Verify changed the file it inspected")
	}
}

// TestVerifySidecarHygiene: sidecars that Verify causes are removed, sidecars
// that already existed are left alone.
func TestVerifySidecarHygiene(t *testing.T) {
	db, _ := newSourceDB(t)
	good := readFile(t, makeBackup(t, db))
	// Present the snapshot as a WAL-mode database so that opening it wants a
	// -shm beside it.
	walMode := slices.Clone(good)
	walMode[18], walMode[19] = 2, 2

	t.Run("created sidecars are removed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "wal.db")
		if err := os.WriteFile(path, walMode, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(context.Background(), path); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if names := listDir(t, dir); !slices.Equal(names, []string{"wal.db"}) {
			t.Fatalf("Verify left files behind: %v", names)
		}
	})
	t.Run("pre-existing sidecars are kept", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "wal.db")
		if err := os.WriteFile(path, walMode, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"-wal", "-shm"} {
			if err := os.WriteFile(path+s, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Verify(context.Background(), path); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		want := []string{"wal.db", "wal.db-shm", "wal.db-wal"}
		if names := listDir(t, dir); !slices.Equal(names, want) {
			t.Fatalf("files = %v, want %v", names, want)
		}
	})
}

// FuzzVerify: arbitrary bytes never panic Verify or Restore, and a file is
// only ever reported ok if it is structurally a SQLite database. Restore must
// leave its target untouched when the source is not ok.
func FuzzVerify(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("SQLite format 3\x00"))
	f.Add(append([]byte("SQLite format 3\x00"), make([]byte, 600)...))
	f.Add(bytes.Repeat([]byte{0}, 4096))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.db")
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := Verify(context.Background(), src)
		if res.IntegrityOK {
			if err != nil {
				t.Fatalf("ok with error: %v", err)
			}
			if len(data) < minDatabaseSize || !bytes.HasPrefix(data, sqliteMagic) {
				t.Fatalf("non-database reported ok (%d bytes)", len(data))
			}
		} else if err == nil {
			t.Fatal("not ok without an error")
		}

		target := filepath.Join(dir, "target.db")
		original := []byte("original target contents")
		if err := os.WriteFile(target, original, 0o600); err != nil {
			t.Fatal(err)
		}
		_, rerr := Restore(context.Background(), target, src, nil)
		if !res.IntegrityOK {
			if rerr == nil {
				t.Fatal("Restore accepted a source Verify rejected")
			}
			if !bytes.Equal(readFile(t, target), original) {
				t.Fatal("failed Restore modified the target")
			}
		}
	})
}
