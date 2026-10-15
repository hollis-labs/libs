// Package tokenfile is the lifecycle of a bearer-token file: a strict reader, an
// atomic create and rotate, a watcher that notices when the file changes, and a
// refresh helper for short-lived tokens that clients re-read.
//
// It is ported, not invented: the reader follows go-tether-client's
// readTokenFile (O_NOFOLLOW, mode exactly 0600, uid check, size cap) and the
// create/rotate/watch parts follow Hadron's internal/localauth/token.go
// (O_EXCL create, temp+fsync+rename rotate, stat-stamp re-read with a digest
// compare, constant-time verify).
//
// The boundary is who can read the file. A process with the same uid can read
// it; this package does not defend against that. An error names the path and
// the rule that failed and never contains the token.
//
// The package depends on the standard library only. Reading and watching need
// POSIX ownership and modes; on other platforms they return [ErrUnsupported].
package tokenfile

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Modes of everything this package creates.
const (
	FileMode os.FileMode = 0o600
	DirMode  os.FileMode = 0o700
)

// DefaultMaxSize bounds a token file, newline included. go-tether-client caps a
// bearer token file at 258 bytes; 4 KiB leaves room for a JWT.
const DefaultMaxSize = 4096

// TokenBytes is the entropy of a generated token (hex-encoded to twice as many characters).
const TokenBytes = 32

var (
	// ErrNotRegular: the path is a symlink, directory, device or pipe.
	ErrNotRegular = errors.New("token file is not a regular file")
	// ErrMode: the mode is not exactly 0600.
	ErrMode = errors.New("token file mode must be exactly 0600")
	// ErrOwner: the file belongs to another user.
	ErrOwner = errors.New("token file is not owned by the current user")
	// ErrTooLarge: the file exceeds the size cap.
	ErrTooLarge = errors.New("token file is too large")
	// ErrEmpty: the file holds no token.
	ErrEmpty = errors.New("token file is empty")
	// ErrInvalid: the content failed the validator.
	ErrInvalid = errors.New("token file content is malformed")
	// ErrChanged: the file was replaced between the check and the open.
	ErrChanged = errors.New("token file changed while it was being opened")
	// ErrUnsupported: this platform has no POSIX ownership and mode.
	ErrUnsupported = errors.New("token files require POSIX ownership and mode 0600 on this platform")
)

// Options tune the reader. The zero value is valid.
type Options struct {
	// MaxSize caps the file in bytes (default [DefaultMaxSize]).
	MaxSize int
	// Validate, if set, must accept the trimmed token; otherwise [ErrInvalid].
	Validate func(token string) bool
}

func (o Options) maxSize() int {
	if o.MaxSize <= 0 {
		return DefaultMaxSize
	}
	return o.MaxSize
}

// Generate returns a new random token: 64 lower-case hex characters.
func Generate() (string, error) {
	raw := make([]byte, TokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// ValidHex reports whether s has the shape [Generate] produces. It is a ready
// Options.Validate.
func ValidHex(s string) bool {
	if len(s) != 2*TokenBytes {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Read returns the token in path, trimmed of surrounding whitespace. It refuses
// a symlink at the final component (O_NOFOLLOW), anything but a regular file,
// a mode other than exactly 0600, a file owned by another uid, a file over the
// size cap, and a file the path no longer names once opened. Checks run on the
// open descriptor, not only on the path.
func Read(path string, opts ...Options) (string, error) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	li, err := os.Lstat(path)
	if err != nil {
		return "", pathErr(path, err)
	}
	if !li.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %w", path, ErrNotRegular)
	}
	f, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, errUnsupported) {
			return "", ErrUnsupported
		}
		return "", pathErr(path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", pathErr(path, err)
	}
	if !os.SameFile(li, fi) {
		return "", fmt.Errorf("%s: %w", path, ErrChanged)
	}
	if cerr := checkInfo(path, fi); cerr != nil {
		return "", cerr
	}
	limit := o.maxSize()
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return "", pathErr(path, err)
	}
	if len(body) > limit {
		return "", fmt.Errorf("%s: %w (limit %d bytes)", path, ErrTooLarge, limit)
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("%s: %w", path, ErrEmpty)
	}
	if o.Validate != nil && !o.Validate(token) {
		return "", fmt.Errorf("%s: %w", path, ErrInvalid)
	}
	return token, nil
}

func checkInfo(path string, fi fs.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: %w", path, ErrNotRegular)
	}
	if perm := fi.Mode().Perm(); perm != FileMode {
		return fmt.Errorf("%s: %w (mode %04o); run chmod 600 on it", path, ErrMode, perm)
	}
	if !ownedByCurrentUser(fi) {
		return fmt.Errorf("%s: %w", path, ErrOwner)
	}
	return nil
}

// pathErr drops the *PathError's copy of the path (the caller adds it) and
// keeps the cause for errors.Is.
func pathErr(path string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("token file %s: %w", path, err)
}

// Create writes token to path as a new file (0600, O_EXCL, never following a
// symlink), creating the parent directories 0700. It returns an error that
// satisfies errors.Is(err, fs.ErrExist) if the file exists.
func Create(path, token string) error {
	if token == "" {
		return ErrEmpty
	}
	if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	f, err := createExclusive(path)
	if err != nil {
		return pathErr(path, err)
	}
	_, werr := f.WriteString(token + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if joined := errors.Join(werr, serr, cerr); joined != nil {
		_ = os.Remove(path)
		return pathErr(path, joined)
	}
	return nil
}

// Ensure returns the token in path, creating the file with generate() (default
// [Generate]) if it does not exist. Two processes racing to create it agree on
// the winner's token.
func Ensure(path string, generate func() (string, error), opts ...Options) (string, error) {
	tok, err := Read(path, opts...)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return tok, err
	}
	if generate == nil {
		generate = Generate
	}
	cand, err := generate()
	if err != nil {
		return "", err
	}
	if err := Create(path, cand); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Read(path, opts...) // another process created it first
		}
		return "", err
	}
	return Read(path, opts...)
}

// Write replaces path with token atomically: a temp file in the same directory
// (0600, O_EXCL), fsync, rename, then a directory sync. A reader sees the old
// token or the new one, never a partial file. If path exists it must be a
// regular file this user may replace (a symlink at path is replaced, not
// followed).
func Write(path, token string) error {
	if token == "" {
		return ErrEmpty
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+"."+hex.EncodeToString(suffix[:])+".tmp")
	f, err := createExclusive(tmp)
	if err != nil {
		return pathErr(tmp, err)
	}
	werr := error(nil)
	if _, werr = f.WriteString(token + "\n"); werr == nil {
		werr = f.Chmod(FileMode)
	}
	serr := f.Sync()
	cerr := f.Close()
	if joined := errors.Join(werr, serr, cerr); joined != nil {
		_ = os.Remove(tmp)
		return pathErr(path, joined)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return pathErr(path, err)
	}
	syncDir(dir)
	return nil
}

// Rotate writes a freshly generated token (generate, default [Generate]) over
// path atomically and returns it. If path exists but is not a valid token file
// (loose mode, wrong owner) Rotate refuses; a missing file is created.
func Rotate(path string, generate func() (string, error), opts ...Options) (string, error) {
	if _, err := Read(path, opts...); err != nil && !errors.Is(err, fs.ErrNotExist) &&
		!errors.Is(err, ErrEmpty) && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrTooLarge) {
		return "", err
	}
	if generate == nil {
		generate = Generate
	}
	tok, err := generate()
	if err != nil {
		return "", err
	}
	if err := Write(path, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// Digest is the SHA-256 of a token. A [Watcher] keeps only this to verify.
func Digest(token string) [sha256.Size]byte { return sha256.Sum256([]byte(token)) }

// Equal compares a presented token with a wanted one in constant time.
func Equal(presented, wanted string) bool {
	p, w := Digest(presented), Digest(wanted)
	return subtle.ConstantTimeCompare(p[:], w[:]) == 1
}

func constantEq(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }
