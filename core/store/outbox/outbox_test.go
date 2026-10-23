package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		// The old worker's ack is a no-op; the new owner's works.
		if err := o.Ack(ctx, s, "w1", a); err != nil {
			t.Fatal(err)
		}
		if n, _ := o.Pending(ctx, s.Reader()); n != 3 {
			t.Fatalf("stale ack delivered the message: pending %d", n)
		}
		if err := o.Ack(ctx, s, "w2", a); err != nil {
			t.Fatal(err)
		}
		if err := o.Ack(ctx, s, "w2", b); err != nil {
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
	if _, err := outbox.New(outbox.Config{Table: "x; DROP TABLE y"}); err == nil {
		t.Error("table name not validated")
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
