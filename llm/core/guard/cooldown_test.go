package guard

import (
	"testing"
	"time"
)

var (
	keyA1 = Key{Resource: "api", Account: "a", Model: "m1"}
	keyA2 = Key{Resource: "api", Account: "a", Model: "m2"}
	keyB1 = Key{Resource: "api", Account: "b", Model: "m1"}
)

func noJitter() Backoff { return Backoff{Base: time.Second, Max: 8 * time.Second} }

func TestBackoffDelay(t *testing.T) {
	b := noJitter()
	want := []time.Duration{time.Second, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for attempt, w := range want {
		if got := b.Delay(attempt); got != w {
			t.Errorf("Delay(%d) = %v, want %v", attempt, got, w)
		}
	}
	if got := b.Delay(1 << 20); got != 8*time.Second {
		t.Errorf("a huge attempt must stay capped, got %v", got)
	}
	b.Jitter, b.Rand = 0.5, func() float64 { return 0.5 }
	if got := b.Delay(2); got != 2500*time.Millisecond {
		t.Errorf("jittered Delay(2) = %v, want 2.5s", got)
	}
	if got := (Backoff{}).Delay(1); got != time.Second {
		t.Errorf("zero Backoff Delay(1) = %v, want 1s", got)
	}
}

func TestCooldownQuotaUsesRetryAfter(t *testing.T) {
	clk := newFakeClock()
	c := NewCooldown(CooldownConfig{Clock: clk})
	c.Record(keyA1, ClassQuota, 42*time.Second)
	if got := c.RetryAfter(keyA1); got != 42*time.Second {
		t.Fatalf("RetryAfter = %v, want the provider's 42s", got)
	}
	if c.RetryAfter(keyA2) != 0 || c.RetryAfter(keyB1) != 0 {
		t.Fatal("a quota cooldown must stay on its own key")
	}
	clk.advance(42 * time.Second)
	if _, _, ok := c.Until(keyA1); ok {
		t.Fatal("cooldown must end at the retry-after")
	}
}

func TestCooldownQuotaDefault(t *testing.T) {
	clk := newFakeClock()
	c := NewCooldown(CooldownConfig{Clock: clk, QuotaCooldown: 10 * time.Second})
	until, ok := c.Record(keyA1, ClassQuota, 0)
	if !ok || until.Sub(clk.Now()) != 10*time.Second {
		t.Fatalf("quota without retry-after: until in %v, want 10s", until.Sub(clk.Now()))
	}
}

func TestCooldownAuthCoolsWholeAccount(t *testing.T) {
	clk := newFakeClock()
	c := NewCooldown(CooldownConfig{Clock: clk, AuthCooldown: time.Minute})
	c.Record(keyA1, ClassAuth, 0)
	until, class, ok := c.Until(keyA2)
	if !ok || class != ClassAuth || until.Sub(clk.Now()) != time.Minute {
		t.Fatalf("auth failure must cool every model of the account: (%v, %v, %v)", until, class, ok)
	}
	if c.RetryAfter(keyB1) != 0 {
		t.Fatal("auth failure must not cool another account")
	}
	c.Success(keyA2)
	if c.RetryAfter(keyA1) != 0 {
		t.Fatal("a success on the account must end its auth cooldown")
	}
}

func TestCooldownTransientBacksOffAndResets(t *testing.T) {
	clk := newFakeClock()
	c := NewCooldown(CooldownConfig{Clock: clk, Backoff: noJitter()})
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		c.Record(keyA1, ClassTransient, 0)
		if got := c.RetryAfter(keyA1); got != want {
			t.Fatalf("failure %d: RetryAfter = %v, want %v", i+1, got, want)
		}
		clk.advance(want)
	}
	c.Success(keyA1)
	c.Record(keyA1, ClassTransient, 0)
	if got := c.RetryAfter(keyA1); got != time.Second {
		t.Fatalf("after Success the backoff must restart, got %v", got)
	}
}

func TestCooldownTransientPrefersRetryAfter(t *testing.T) {
	c := NewCooldown(CooldownConfig{Clock: newFakeClock(), Backoff: noJitter()})
	c.Record(keyA1, ClassTransient, 30*time.Second)
	if got := c.RetryAfter(keyA1); got != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want the provider's 30s", got)
	}
}

func TestCooldownIgnoresRequestConnectionCanceled(t *testing.T) {
	c := NewCooldown(CooldownConfig{Clock: newFakeClock()})
	for _, class := range []Class{ClassRequest, ClassConnection, ClassCanceled} {
		if _, ok := c.Record(keyA1, class, time.Minute); ok {
			t.Errorf("%v must set no cooldown", class)
		}
	}
	if c.RetryAfter(keyA1) != 0 {
		t.Fatal("expected no cooldown")
	}
}

func TestCooldownNeverShortens(t *testing.T) {
	clk := newFakeClock()
	c := NewCooldown(CooldownConfig{Clock: clk})
	c.Record(keyA1, ClassQuota, time.Hour)
	c.Record(keyA1, ClassQuota, time.Second)
	c.Set(keyA1, ClassQuota, clk.Now().Add(time.Minute))
	if got := c.RetryAfter(keyA1); got != time.Hour {
		t.Fatalf("RetryAfter = %v, a shorter cooldown must not replace a longer one", got)
	}
}

func TestCooldownResourceScopeAndSet(t *testing.T) {
	clk := newFakeClock()
	c := NewCooldown(CooldownConfig{Clock: clk})
	c.Set(Key{Resource: "api"}, ClassTransient, clk.Now().Add(time.Minute))
	for _, k := range []Key{keyA1, keyB1} {
		if got := c.RetryAfter(k); got != time.Minute {
			t.Fatalf("resource-wide cooldown must apply to %v, got %v", k, got)
		}
	}
	c.Success(keyA1)
	if c.RetryAfter(keyB1) != time.Minute {
		t.Fatal("one account's success must not end a resource-wide cooldown")
	}
	c.Clear(Key{Resource: "api"})
	if c.RetryAfter(keyB1) != 0 {
		t.Fatal("Clear must drop the cooldown")
	}
}
