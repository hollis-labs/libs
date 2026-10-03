//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTryAcquireRefusesDoubleAcquire(t *testing.T) {
	path := tmpPath(t, "sub/d.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()

	_, err = TryAcquire(path)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("second TryAcquire = %v, want *HeldError", err)
	}
	if held.HolderPID != os.Getpid() || held.Path != path {
		t.Fatalf("HeldError = %+v, want pid %d", held, os.Getpid())
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatal("HeldError should match ErrAlreadyRunning")
	}
}

func TestTryAcquireRefusedWhileHeldByAnotherProcess(t *testing.T) {
	path := tmpPath(t, "d.lock")
	h := startHelper(t, "hold-lock", path)
	_, err := TryAcquire(path)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("TryAcquire = %v, want *HeldError", err)
	}
	if held.HolderPID != h.pid() {
		t.Fatalf("HolderPID = %d, want helper pid %d", held.HolderPID, h.pid())
	}
}

// TestLockReleasedOnSIGKILLOfHolder is the property the whole package rests
// on: a holder killed with no chance to clean up must not leave the lock held.
func TestLockReleasedOnSIGKILLOfHolder(t *testing.T) {
	path := tmpPath(t, "d.lock")
	h := startHelper(t, "hold-lock", path)
	if _, err := TryAcquire(path); err == nil {
		t.Fatal("lock should be held by helper")
	}
	if err := h.cmd.Process.Kill(); err != nil { // SIGKILL, our own child
		t.Fatal(err)
	}
	if sig := h.exitSignal(t); sig != 9 {
		t.Fatalf("helper exit signal = %d, want 9 (SIGKILL)", sig)
	}
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("lock still held after holder was SIGKILLed: %v", err)
	}
	_ = l.Release()
}

func TestTryAcquireConcurrentExactlyOneWins(t *testing.T) {
	path := tmpPath(t, "d.lock")
	const n = 16
	var wins atomic.Int32
	var held atomic.Int32
	var locks []*Lock
	var mu sync.Mutex
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l, err := TryAcquire(path)
			var he *HeldError
			switch {
			case err == nil:
				wins.Add(1)
				mu.Lock()
				locks = append(locks, l)
				mu.Unlock()
			case errors.As(err, &he):
				held.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || held.Load() != n-1 {
		t.Fatalf("wins=%d held=%d, want 1 and %d", wins.Load(), held.Load(), n-1)
	}
	for _, l := range locks {
		_ = l.Release()
	}
}

func TestReleaseAllowsReacquireAndIsIdempotent(t *testing.T) {
	path := tmpPath(t, "d.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Release(); err != nil {
		t.Fatal(err)
	}
	if err = l.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	var nilLock *Lock
	if err = nilLock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("lock file must be left on disk: %v", err)
	}
	l2, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	_ = l2.Release()
}

func TestAcquireWaitsForReleaseAndHonorsContext(t *testing.T) {
	path := tmpPath(t, "d.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err = Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire on held lock = %v, want DeadlineExceeded", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = l.Release()
	}()
	l2, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = l2.Release()
}

func TestSetInfoIsVisibleToContenders(t *testing.T) {
	path := tmpPath(t, "d.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err = l.SetInfo([]byte("v1 http://127.0.0.1:9\nline2")); err != nil {
		t.Fatal(err)
	}
	_, err = TryAcquire(path)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatal(err)
	}
	if string(held.Info) != "v1 http://127.0.0.1:9\nline2" || held.HolderPID != os.Getpid() {
		t.Fatalf("HeldError = pid %d info %q", held.HolderPID, held.Info)
	}
	_ = l.Release()
	if err := l.SetInfo(nil); err == nil {
		t.Fatal("SetInfo after Release should fail")
	}
}

// TestLockNotInheritedByChild: a spawned child must not hold the parent's
// lock (the fd is close-on-exec), or killing the parent would not free it.
func TestLockNotInheritedByChild(t *testing.T) {
	path := tmpPath(t, "d.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	h := startHelper(t, "sleep", "")
	_ = l.Release()
	l2, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("child that started while lock was held keeps it: %v", err)
	}
	_ = l2.Release()
	_ = h
}
