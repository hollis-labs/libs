package localdaemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitReadySucceedsAfterPolling(t *testing.T) {
	var n atomic.Int32
	err := WaitReady(context.Background(), 5*time.Second, 5*time.Millisecond, func(context.Context) (bool, error) {
		return n.Add(1) >= 4, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 4 {
		t.Fatalf("check ran %d times, want 4", n.Load())
	}
}

func TestWaitReadyChecksImmediately(t *testing.T) {
	start := time.Now()
	err := WaitReady(context.Background(), 5*time.Second, time.Hour, func(context.Context) (bool, error) { return true, nil })
	if err != nil || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	start := time.Now()
	err := WaitReady(context.Background(), 150*time.Millisecond, 10*time.Millisecond, func(context.Context) (bool, error) { return false, nil })
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if el := time.Since(start); el < 140*time.Millisecond || el > 2*time.Second {
		t.Fatalf("elapsed %v", el)
	}
}

func TestWaitReadyCheckErrorIsFatal(t *testing.T) {
	boom := errors.New("child exited")
	var n atomic.Int32
	err := WaitReady(context.Background(), 5*time.Second, 5*time.Millisecond, func(context.Context) (bool, error) {
		n.Add(1)
		return false, boom
	})
	if !errors.Is(err, boom) || n.Load() != 1 {
		t.Fatalf("err = %v after %d checks", err, n.Load())
	}
}

func TestWaitReadyParentContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	err := WaitReady(ctx, 10*time.Second, 5*time.Millisecond, func(context.Context) (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestWaitReadyBoundsSlowCheckByDeadline(t *testing.T) {
	start := time.Now()
	err := WaitReady(context.Background(), 100*time.Millisecond, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		<-ctx.Done()
		return false, nil
	})
	if !errors.Is(err, ErrNotReady) || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
}

func TestWaitReadyRejectsBadArguments(t *testing.T) {
	ok := func(context.Context) (bool, error) { return true, nil }
	if WaitReady(context.Background(), 0, time.Millisecond, ok) == nil {
		t.Fatal("zero deadline must be rejected")
	}
	if WaitReady(context.Background(), time.Second, time.Millisecond, nil) == nil {
		t.Fatal("nil check must be rejected")
	}
}
