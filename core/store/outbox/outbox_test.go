package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/libs/core/panics"
	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/outbox"
	"github.com/hollis-labs/libs/core/store/storetest"
)

var ctx = context.Background()

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T, s *store.Store, cfg outbox.Config) (*outbox.Outbox, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	cfg.Now = clk.now
	cfg.Guard = panics.New(panics.Config{Reporter: panics.ReporterFunc(func(panics.Record) {}), Redact: panics.Unredacted})
	o, err := outbox.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range o.DDL(s.Dialect()) {
		storetest.Exec(t, s, stmt)
	}
	return o, clk
}

func enqueue(t *testing.T, s *store.Store, o *outbox.Outbox, topic string, payload string, opts ...outbox.EnqueueOption) string {
	t.Helper()
	var id string
	err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		id, err = o.Enqueue(ctx, tx, topic, []byte(payload), opts...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEnqueueIsTransactional(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, _ := setup(t, s, outbox.Config{})
		_ = s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			if _, err := o.Enqueue(ctx, tx, "t", []byte("lost")); err != nil {
				return err
			}
			return errors.New("business change failed")
		})
		if n, _ := o.Pending(ctx, s.Reader()); n != 0 {
			t.Fatalf("rolled-back enqueue left %d messages", n)
		}
		enqueue(t, s, o, "t", "kept")
		if n, _ := o.Pending(ctx, s.Reader()); n != 1 {
			t.Fatalf("pending = %d", n)
		}
	})
}

func TestClaimOrderLeaseAndAck(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, clk := setup(t, s, outbox.Config{Lease: time.Minute})
		a := enqueue(t, s, o, "t", "a")
		clk.add(time.Millisecond)
		b := enqueue(t, s, o, "t", "b")
		clk.add(time.Millisecond)
		later := enqueue(t, s, o, "t", "later", outbox.WithAvailableAt(clk.now().Add(time.Hour)))

		got, err := o.Claim(ctx, s, "w1", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ID != a || got[1].ID != b || string(got[0].Payload) != "a" || got[0].Attempts != 1 {
			t.Fatalf("claim = %+v", got)
		}
		if again, _ := o.Claim(ctx, s, "w2", 10); len(again) != 0 {
			t.Fatalf("leased messages were claimed again: %+v", again)
		}
		// Lease expires: another worker takes over.
		clk.add(2 * time.Minute)
		again, _ := o.Claim(ctx, s, "w2", 10)
		if len(again) != 2 || again[0].Attempts != 2 {
			t.Fatalf("after lease expiry: %+v", again)
		}
		// The old worker's ack is refused; the new owner's works.
		if err := o.Ack(ctx, s, got[0]); !errors.Is(err, outbox.ErrLeaseLost) {
			t.Fatalf("stale ack: %v", err)
		}
		if n, _ := o.Pending(ctx, s.Reader()); n != 3 {
			t.Fatalf("stale ack delivered the message: pending %d", n)
		}
		if err := o.Ack(ctx, s, again[0]); err != nil {
			t.Fatal(err)
		}
		if err := o.Ack(ctx, s, again[1]); err != nil {
			t.Fatal(err)
		}
		clk.add(2 * time.Hour)
		last, _ := o.Claim(ctx, s, "w2", 10)
		if len(last) != 1 || last[0].ID != later {
			t.Fatalf("delayed message: %+v", last)
		}
		n, err := o.Purge(ctx, s, clk.now())
		if err != nil || n != 2 {
			t.Fatalf("purge = %d, %v", n, err)
		}
	})
}

func TestLeaseLostMidBatchDoesNotDoubleDeliver(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, clk := setup(t, s, outbox.Config{Lease: time.Minute})
		enqueue(t, s, o, "t", "a")
		clk.add(time.Millisecond)
		enqueue(t, s, o, "t", "b")
		var ran []string
		n, err := o.Drain(ctx, s, "slow", 10, func(ctx context.Context, m outbox.Message) error {
			ran = append(ran, string(m.Payload))
			if dl, ok := ctx.Deadline(); !ok || dl.IsZero() {
				t.Error("handler context must end with the lease")
			}
			clk.add(2 * time.Minute) // the handler overruns the lease
			return nil
		})
		if err != nil || n != 0 {
			t.Fatalf("n=%d err=%v: an ack after the lease ended must not count", n, err)
		}
		if len(ran) != 1 {
			t.Fatalf("handler ran for %v; the second message's lease was already over and must be skipped", ran)
		}
		// Both are claimable again, so nothing is lost.
		if again, _ := o.Claim(ctx, s, "other", 10); len(again) != 2 {
			t.Fatalf("reclaim = %d", len(again))
		}
	})
}

func TestStaleFailFromAReusedWorkerNameIsRefused(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, clk := setup(t, s, outbox.Config{Lease: time.Minute})
		enqueue(t, s, o, "t", "x")
		first, _ := o.Claim(ctx, s, "worker-1", 1)
		clk.add(2 * time.Minute)
		second, _ := o.Claim(ctx, s, "worker-1", 1) // same name, new claim
		if len(first) != 1 || len(second) != 1 {
			t.Fatalf("claims: %d %d", len(first), len(second))
		}
		if err := o.Fail(ctx, s, first[0], errors.New("late")); !errors.Is(err, outbox.ErrLeaseLost) {
			t.Fatalf("stale fail: %v", err)
		}
		// The second claim is untouched: nobody else can take it, and it can ack.
		if third, _ := o.Claim(ctx, s, "worker-3", 1); len(third) != 0 {
			t.Fatalf("stale fail released the live claim")
		}
		if err := o.Ack(ctx, s, second[0]); err != nil {
			t.Fatal(err)
		}
	})
}

func TestFailRecordsAwkwardErrorText(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, _ := setup(t, s, outbox.Config{})
		id := enqueue(t, s, o, "t", "x")
		bad := strings.Repeat("é", 400) + "\x00\xff\xfe tail" // multi-byte, NUL and invalid UTF-8
		n, err := o.Drain(ctx, s, "w", 1, func(context.Context, outbox.Message) error { return errors.New(bad) })
		if err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		var last string
		if err := s.QueryRowContext(ctx, `SELECT last_error FROM outbox_messages WHERE id = ?`, id).Scan(&last); err != nil {
			t.Fatal(err)
		}
		if last == "" || len(last) > 500 || !utf8.ValidString(last) || strings.ContainsRune(last, 0) {
			t.Fatalf("last_error = %q (%d bytes)", last, len(last))
		}
	})
}

func TestParkedMessagesCanBeListedAndRetried(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, clk := setup(t, s, outbox.Config{MaxAttempts: 1, Backoff: func(int) time.Duration { return 0 }})
		id := enqueue(t, s, o, "t", "x")
		_, _ = o.Drain(ctx, s, "w", 1, func(context.Context, outbox.Message) error { return errors.New("down") })
		clk.add(time.Second)
		if n, _ := o.Pending(ctx, s.Reader()); n != 0 {
			t.Fatalf("a parked message is not pending: %d", n)
		}
		parked, err := o.Parked(ctx, s.Reader(), 10)
		if err != nil || len(parked) != 1 || parked[0].ID != id || parked[0].LastError != "down" {
			t.Fatalf("parked = %+v, %v", parked, err)
		}
		if ok, err := o.Retry(ctx, s, id); err != nil || !ok {
			t.Fatalf("retry = %v, %v", ok, err)
		}
		n, _ := o.Drain(ctx, s, "w", 1, func(context.Context, outbox.Message) error { return nil })
		if n != 1 {
			t.Fatalf("retried message not delivered: %d", n)
		}
		if ok, _ := o.Retry(ctx, s, id); ok {
			t.Fatal("a delivered message must not be retried")
		}
	})
}

func TestEnqueueNilPayload(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, _ := setup(t, s, outbox.Config{})
		err := s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
			_, err := o.Enqueue(ctx, tx, "t", nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestDrainRetriesBacksOffAndParks(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, clk := setup(t, s, outbox.Config{MaxAttempts: 3, Backoff: func(int) time.Duration { return time.Minute }})
		id := enqueue(t, s, o, "t", "x")
		fail := func(context.Context, outbox.Message) error { return errors.New("down") }

		for i := 0; i < 3; i++ {
			n, err := o.Drain(ctx, s, "w", 5, fail)
			if err != nil || n != 0 {
				t.Fatalf("attempt %d: n=%d err=%v", i, n, err)
			}
			// Backoff: not claimable until the retry time.
			if m, _ := o.Claim(ctx, s, "w", 5); len(m) != 0 {
				t.Fatalf("claimed inside backoff window")
			}
			clk.add(2 * time.Minute)
		}
		// Attempts exhausted: parked.
		if m, _ := o.Claim(ctx, s, "w", 5); len(m) != 0 {
			t.Fatalf("parked message was claimed")
		}
		var attempts int
		var last string
		_ = s.QueryRowContext(ctx, `SELECT attempts, last_error FROM outbox_messages WHERE id = ?`, id).Scan(&attempts, &last)
		if attempts != 3 || last != "down" {
			t.Fatalf("attempts=%d last_error=%q", attempts, last)
		}
	})
}

func TestDrainContainsHandlerPanics(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, _ := setup(t, s, outbox.Config{})
		id := enqueue(t, s, o, "t", "x")
		n, err := o.Drain(ctx, s, "w", 5, func(context.Context, outbox.Message) error { panic("secret-token-123") })
		if err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		var last string
		_ = s.QueryRowContext(ctx, `SELECT last_error FROM outbox_messages WHERE id = ?`, id).Scan(&last)
		if last == "" || contains(last, "secret-token") {
			t.Fatalf("last_error = %q (must carry a ref, not the panic value)", last)
		}
	})
}

func TestConcurrentDrainersDeliverEachMessageOnce(t *testing.T) {
	storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
		o, _ := setup(t, s, outbox.Config{Lease: time.Hour})
		const total = 60
		for i := 0; i < total; i++ {
			enqueue(t, s, o, "t", fmt.Sprint(i))
		}
		var seen sync.Map
		var dup, delivered atomic.Int64
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				name := fmt.Sprintf("w%d", w)
				for {
					n, err := o.Drain(ctx, s, name, 7, func(_ context.Context, m outbox.Message) error {
						if _, loaded := seen.LoadOrStore(m.ID, true); loaded {
							dup.Add(1)
						}
						return nil
					})
					if err != nil {
						t.Error(err)
						return
					}
					delivered.Add(int64(n))
					if n == 0 {
						return
					}
				}
			}(w)
		}
		wg.Wait()
		if dup.Load() != 0 || delivered.Load() != total {
			t.Fatalf("delivered %d of %d, duplicates %d", delivered.Load(), total, dup.Load())
		}
	})
}

func TestClaimSQLPerDialect(t *testing.T) {
	o, _ := outbox.New(outbox.Config{})
	pg, _ := store.DialectFor(store.DriverPostgres)
	lite, _ := store.DialectFor(store.DriverSQLite)
	if !contains(o.ClaimSQL(pg), "FOR UPDATE SKIP LOCKED") {
		t.Error("postgres claim must use SKIP LOCKED")
	}
	if contains(o.ClaimSQL(lite), "SKIP LOCKED") {
		t.Error("sqlite claim must not")
	}
	for _, bad := range []string{"x; DROP TABLE y", "app.outbox"} {
		if _, err := outbox.New(outbox.Config{Table: bad}); err == nil {
			t.Errorf("table name %q not rejected", bad)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
