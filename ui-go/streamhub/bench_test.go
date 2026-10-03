package streamhub_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

// BenchmarkPublishFanout measures Publish latency with 100 fast subscribers
// and, optionally, one subscriber that never reads. The slow subscriber's
// policy bounds the cost: under DropOldest and CloseAndResume Publish must
// stay in the same range as with no slow subscriber ("max-ns" is the worst
// single Publish observed).
func BenchmarkPublishFanout(b *testing.B) {
	cases := []struct {
		name string
		slow *streamhub.SlowPolicy
	}{
		{"100fast", nil},
		{"100fast+1slow/DropOldest", &streamhub.DropOldest},
		{"100fast+1slow/DropNewest", &streamhub.DropNewest},
		{"100fast+1slow/CloseAndResume", &streamhub.CloseAndResume},
		{"100fast+1slow/EvictAfterN64", ptr(streamhub.EvictAfterN(64))},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			ctx := context.Background()
			h := streamhub.New(streamhub.NewMemoryLog(), streamhub.WithRetention(streamhub.Retention{MaxRecords: 1024}))
			defer h.Shutdown(ctx)

			var wg sync.WaitGroup
			var received atomic.Int64
			for range 100 {
				sub, err := h.Subscribe(ctx, "s", streamhub.SubscribeOptions{Buffer: 4096, Policy: streamhub.DropOldest})
				if err != nil {
					b.Fatal(err)
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						if _, err := sub.Next(ctx); err != nil {
							return
						}
						received.Add(1)
					}
				}()
			}
			if c.slow != nil {
				if _, err := h.Subscribe(ctx, "s", streamhub.SubscribeOptions{Buffer: 64, Policy: *c.slow}); err != nil {
					b.Fatal(err)
				}
			}
			ev := streamhub.Event{Name: "delta", Data: make([]byte, 128)}
			var worst time.Duration
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				t0 := time.Now()
				if _, err := h.Publish(ctx, "s", ev); err != nil {
					b.Fatal(err)
				}
				if d := time.Since(t0); d > worst {
					worst = d
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(worst.Nanoseconds()), "max-ns")
			if err := h.Shutdown(ctx); err != nil {
				b.Fatal(err)
			}
			wg.Wait()
			_ = fmt.Sprint(received.Load())
		})
	}
}

func ptr[T any](v T) *T { return &v }
