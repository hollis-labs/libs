package tokenfile

import (
	"context"
	"crypto/sha256"
	"os"
	"sync"
	"time"
)

// stamp is what makes a Watcher re-read the file.
type stamp struct {
	exists  bool
	modTime int64
	size    int64
	ino     uint64
	mode    os.FileMode
	uid     uint32
}

// Watcher follows a token file. Each call stats the file; only when the stamp
// differs from the last look does it read again, and it compares digests so a
// rewrite with the same token is not a change. A file that fails any check
// holds no token (fail closed) until it is fixed.
type Watcher struct {
	path     string
	opts     Options
	onChange func(err error)

	mu     sync.Mutex
	stamp  stamp
	loaded bool
	token  string
	digest [sha256.Size]byte
	valid  bool
	err    error
}

// NewWatcher follows path. onChange, if set, runs (outside the lock) after the
// token changed, was removed, or became unreadable; err is nil when a new valid
// token is in place. It does not run for the first load.
func NewWatcher(path string, opts Options, onChange func(err error)) *Watcher {
	return &Watcher{path: path, opts: opts, onChange: onChange}
}

// Path is the file being followed.
func (w *Watcher) Path() string { return w.path }

// Token returns the current token, re-reading the file if it changed since the
// last call. The error is the reason the file is not trusted.
func (w *Watcher) Token() (string, error) {
	w.mu.Lock()
	changed, cerr := w.refresh()
	tok, err := w.token, w.err
	cb := w.onChange
	w.mu.Unlock()
	if changed && cb != nil {
		cb(cerr)
	}
	if err != nil {
		return "", err
	}
	return tok, nil
}

// Verify reports whether presented is the current token, in constant time. A
// file that is not trusted verifies nothing.
func (w *Watcher) Verify(presented string) bool {
	if presented == "" {
		return false
	}
	w.mu.Lock()
	changed, cerr := w.refresh()
	valid, want := w.valid, w.digest
	cb := w.onChange
	w.mu.Unlock()
	if changed && cb != nil {
		cb(cerr)
	}
	got := Digest(presented)
	return valid && constantEq(got[:], want[:])
}

// refresh must be called with the lock held. It reports whether the token
// state changed after the first load.
func (w *Watcher) refresh() (changed bool, err error) {
	cur := stampOf(w.path)
	if w.loaded && cur == w.stamp {
		return false, nil
	}
	prevDigest, hadPrev := w.digest, w.valid
	w.loaded, w.stamp = true, cur
	tok, rerr := Read(w.path, w.opts)
	if rerr != nil {
		w.token, w.digest, w.valid, w.err = "", [sha256.Size]byte{}, false, rerr
	} else {
		w.token, w.digest, w.valid, w.err = tok, Digest(tok), true, nil
	}
	if hadPrev && (!w.valid || w.digest != prevDigest) {
		return true, w.err
	}
	return false, nil
}

// RefreshIfOlder is the helper for short-lived tokens that clients re-read. If
// path is missing, untrusted, or last written more than maxAge ago, it writes a
// new token atomically (see [Write]) and returns it with rotated true;
// otherwise it returns the current token. Clients using a [Watcher] or [Read]
// pick the new token up on their next call. now defaults to time.Now.
func RefreshIfOlder(path string, maxAge time.Duration, generate func() (string, error), opts ...Options) (token string, rotated bool, err error) {
	return refreshIfOlder(path, maxAge, generate, time.Now, opts...)
}

func refreshIfOlder(path string, maxAge time.Duration, generate func() (string, error), now func() time.Time, opts ...Options) (string, bool, error) {
	tok, rerr := Read(path, opts...)
	if rerr == nil {
		if fi, err := os.Lstat(path); err == nil && now().Sub(fi.ModTime()) <= maxAge {
			return tok, false, nil
		}
	}
	if generate == nil {
		generate = Generate
	}
	next, err := generate()
	if err != nil {
		return "", false, err
	}
	if err := Write(path, next); err != nil {
		return "", false, err
	}
	return next, true, nil
}

// Keep rotates path in place every ttl until ctx is done: the process that owns
// a short-lived token calls it once. onRotate, if set, receives each new token
// (for example to hand it to a token verifier) and any write error. The first
// write happens immediately if the file is missing or older than ttl.
func Keep(ctx context.Context, path string, ttl time.Duration, generate func() (string, error), onRotate func(token string, err error), opts ...Options) {
	step := func() {
		tok, rotated, err := RefreshIfOlder(path, ttl, generate, opts...)
		if (rotated || err != nil) && onRotate != nil {
			onRotate(tok, err)
		}
	}
	step()
	iv := ttl / 2
	if iv < time.Millisecond {
		iv = time.Millisecond
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			step()
		}
	}
}
