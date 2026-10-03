package sqlitebackup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hollis-labs/libs/util/sqlite/sqlitekit"
)

var sqliteMagic = []byte("SQLite format 3\x00")

// minDatabaseSize is the smallest file that can be a SQLite database: one
// minimum-size page (512 bytes). Anything shorter, including the empty file
// SQLite would happily treat as a fresh database, is not a backup.
const minDatabaseSize = 512

// Verify opens path read-only and runs PRAGMA integrity_check, then takes the
// file's size and SHA-256 (after the check, see Backup).
//
// The file is never modified. Any -wal/-shm sidecar that opening it causes is
// removed again; sidecars that existed beforehand are left alone.
//
// A file that fails verification returns the Result (IntegrityOK false,
// Damage set) and an error wrapping ErrIntegrity, so a caller that checks
// either signal cannot mistake a damaged file for a good one. A file that
// cannot be examined at all (missing, a directory, canceled context) returns
// a plain error.
func Verify(ctx context.Context, path string, opts ...Option) (Result, error) {
	o := newOptions(opts)
	abs, err := resolve(path)
	if err != nil {
		return Result{}, err
	}
	res := Result{Path: abs}
	damage, err := checkIntegrity(ctx, abs, &o)
	if err != nil {
		return res, err
	}
	// Checksum after verification, as in Backup.
	res.SizeBytes, res.SHA256, err = statDigest(abs)
	if err != nil {
		return res, err
	}
	if len(damage) > 0 {
		res.Damage = damage
		return res, fmt.Errorf("%w: %q: %s", ErrIntegrity, abs, strings.Join(damage, "; "))
	}
	res.IntegrityOK = true
	return res, nil
}

// checkIntegrity returns the reasons path is not a sound database (nil when
// it is sound) and a non-nil error only when the file could not be examined.
func checkIntegrity(ctx context.Context, path string, o *options) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("sqlitebackup: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("sqlitebackup: %q is not a regular file", path)
	}
	if info.Size() < minDatabaseSize {
		return []string{fmt.Sprintf("file is %d bytes, too small to be a SQLite database", info.Size())}, nil
	}
	if ok, hErr := hasMagic(path); hErr != nil {
		return nil, hErr
	} else if !ok {
		return []string{"file does not begin with the SQLite header"}, nil
	}

	cleanup := noteSidecars(path)
	defer cleanup()

	db, err := sqlitekit.OpenReadOnly(ctx, path, sqlitekit.OpenOptions{
		Options:      sqlitekit.Options{BusyTimeout: sqlitekit.DefaultBusyTimeout},
		DriverName:   o.driver,
		MaxOpenConns: 1,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []string{"open: " + err.Error()}, nil
	}
	defer func() { _ = db.Close() }()

	if hErr := o.step("verifying", path); hErr != nil {
		return nil, hErr
	}

	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []string{"integrity_check: " + err.Error()}, nil
	}
	defer func() { _ = rows.Close() }()

	var problems []string
	sawRow := false
	for rows.Next() {
		var line string
		if scanErr := rows.Scan(&line); scanErr != nil {
			return append(problems, "integrity_check: "+scanErr.Error()), nil
		}
		sawRow = true
		if strings.TrimSpace(line) != "ok" {
			problems = append(problems, line)
		}
	}
	if rErr := rows.Err(); rErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		problems = append(problems, "integrity_check: "+rErr.Error())
	}
	if !sawRow && len(problems) == 0 {
		problems = append(problems, "integrity_check returned no result")
	}
	return problems, nil
}

func hasMagic(path string) (bool, error) {
	f, err := os.Open(path) //nolint:gosec // caller-named path
	if err != nil {
		return false, fmt.Errorf("sqlitebackup: %w", err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, fmt.Errorf("sqlitebackup: read header: %w", err)
	}
	return bytes.Equal(buf, sqliteMagic), nil
}
