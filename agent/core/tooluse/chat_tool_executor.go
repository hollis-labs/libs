// Package tooluse schedules native tool batches after host permission checks.
package tooluse

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// RunBounded calls run(i) for every i in [0,n) on at most limit goroutines,
// starting items in index order, and returns when all have finished. Results
// are the caller's to store by index, so ordering is unaffected by which
// worker ran what.
//
// Cancellation: an item that has not started when ctx is done is not run;
// skipped(i) is called instead so the caller can fill its result slot (a
// missing tool_result would leave a dangling tool_use). Items already running
// are not interrupted here — run sees the same ctx and finishes as it always
// has — and RunBounded still waits for them, so nothing outlives the call.
// A panic in run(i) is recovered and confined to that item.
func RunBounded(ctx context.Context, n, limit int, run, skipped func(i int)) {
	runBounded(ctx, n, limit, run, skipped, nil)
}

func runBounded(ctx context.Context, n, limit int, run, skipped func(i int), reporter PanicReporter) {
	if n <= 0 {
		return
	}
	if limit <= 0 || limit > n {
		limit = n
	}
	var (
		next atomic.Int64
		wg   sync.WaitGroup
	)
	for w := 0; w < limit; w++ {
		wg.Add(1)
		go func() {
			defer contain(ctx, "service.chat.executeSingleTool.concurrent", reporter)
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				if ctx.Err() != nil {
					skipped(i)
					continue
				}
				call(ctx, "service.chat.executeSingleTool.item", reporter, func() { run(i) })
			}
		}()
	}
	wg.Wait()
}

// PanicReporter lets an embedding host retain its tracing and panic observer.
// The library contains each panic before reporting it; observers must not panic.
type PanicReporter func(context.Context, string, any, []byte)

func call(ctx context.Context, label string, reporter PanicReporter, fn func()) {
	defer contain(ctx, label, reporter)
	fn()
}

func contain(ctx context.Context, label string, reporter PanicReporter) {
	if value := recover(); value != nil {
		stack := debug.Stack()
		if reporter != nil {
			reporter(ctx, label, value, stack)
		} else {
			slog.Error("tooluse: recovered panic", "label", label, "panic", value, "stack", string(stack))
		}
	}
}

// Job describes permission-checked scheduling metadata. Ready is supplied by
// host policy; Concurrent never grants permission to execute a blocked call.
type Job struct{ Ready, Concurrent bool }

// Batch executes ready concurrent jobs within the host limit, then serial jobs
// in input order. Results remain host-owned index slots. Skipped is invoked for
// queued concurrent calls cancelled before execution, preserving tool pairing.
// Running jobs see the same context and are joined before Batch returns.
func Batch(ctx context.Context, jobs []Job, limit int, execute func(index int, concurrent bool), skipped func(index int), reporter PanicReporter) {
	var concurrent, serial []int
	for i, job := range jobs {
		if !job.Ready {
			continue
		}
		if job.Concurrent {
			concurrent = append(concurrent, i)
		} else {
			serial = append(serial, i)
		}
	}
	runBounded(ctx, len(concurrent), limit,
		func(i int) { execute(concurrent[i], true) },
		func(i int) { skipped(concurrent[i]) }, reporter)
	for _, i := range serial {
		execute(i, false)
	}
}
