package guard

import (
	"math/rand/v2"
	"sync"
	"time"
)

// Backoff computes capped exponential delays with optional jitter, for a
// resource that failed without saying when to come back.
type Backoff struct {
	// Base is the first delay. At or below zero selects one second.
	Base time.Duration
	// Max caps the exponential part. At or below zero selects five minutes.
	Max time.Duration
	// Jitter is the largest fraction of the delay added at random, so that
	// callers that failed together do not retry together. Zero adds none;
	// values above 1 are treated as 1.
	Jitter float64
	// Rand returns a number in [0, 1). Nil selects math/rand/v2.Float64.
	Rand func() float64
}

// DefaultBackoff is the Backoff a zero CooldownConfig uses.
var DefaultBackoff = Backoff{Base: time.Second, Max: 5 * time.Minute, Jitter: 0.2}

// Delay returns the delay before retry number attempt, counting from 1:
// Base doubled attempt-1 times, capped at Max, plus up to Jitter of itself.
// An attempt below 1 is treated as 1.
func (b Backoff) Delay(attempt int) time.Duration {
	base, ceiling := b.Base, b.Max
	if base <= 0 {
		base = time.Second
	}
	if ceiling <= 0 {
		ceiling = 5 * time.Minute
	}
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt && d < ceiling; i++ {
		d *= 2
	}
	d = min(d, ceiling)
	if j := min(b.Jitter, 1); j > 0 {
		r := rand.Float64
		if b.Rand != nil {
			r = b.Rand
		}
		d += time.Duration(float64(d) * j * r())
	}
	return d
}

// Key names what a cooldown applies to. Resource is the endpoint or provider,
// Account the credential used against it, Model the model asked for. Empty
// fields are wider scopes: Key{Resource: r, Account: a} is the whole account.
type Key struct {
	Resource string
	Account  string
	Model    string
}

func (k Key) account() Key  { return Key{Resource: k.Resource, Account: k.Account} }
func (k Key) resource() Key { return Key{Resource: k.Resource} }

// CooldownConfig configures a Cooldown. The zero value selects every default.
type CooldownConfig struct {
	// Backoff gives the delay after a transient failure that named no
	// retry-after. The zero Backoff selects DefaultBackoff.
	Backoff Backoff
	// QuotaCooldown is the wait after a quota error that named no
	// retry-after. At or below zero selects one minute.
	QuotaCooldown time.Duration
	// AuthCooldown is the wait after an auth error that named no
	// retry-after. At or below zero selects five minutes.
	AuthCooldown time.Duration
	// Clock is the time source. Nil selects SystemClock.
	Clock Clock
}

// Cooldown records, per Key, when a resource may next be tried. It answers
// "when may we retry"; it does not count budget. Safe for concurrent use.
//
// What is cooled down depends on the class of the failure:
//
//   - ClassQuota cools the key down for the provider's retry-after, or
//     QuotaCooldown.
//   - ClassAuth cools the whole account down (the key without its Model) for
//     the retry-after, or AuthCooldown.
//   - ClassTransient and ClassUnknown cool the key down for the retry-after,
//     or the next Backoff delay; consecutive failures lengthen it.
//   - ClassConnection, ClassRequest and ClassCanceled set nothing: a broken
//     connection is the breaker's business, and a malformed request must not
//     cool down a healthy account.
//
// A new cooldown never shortens one already in force.
type Cooldown struct {
	clk     Clock
	backoff Backoff
	quota   time.Duration
	auth    time.Duration

	mu sync.Mutex
	m  map[Key]*cooldownEntry
}

type cooldownEntry struct {
	until  time.Time
	class  Class
	streak int // consecutive transient failures, reset by Success
}

// NewCooldown returns an empty Cooldown configured by cfg.
func NewCooldown(cfg CooldownConfig) *Cooldown {
	if b := cfg.Backoff; b.Base == 0 && b.Max == 0 && b.Jitter == 0 && b.Rand == nil {
		cfg.Backoff = DefaultBackoff
	}
	if cfg.QuotaCooldown <= 0 {
		cfg.QuotaCooldown = time.Minute
	}
	if cfg.AuthCooldown <= 0 {
		cfg.AuthCooldown = 5 * time.Minute
	}
	return &Cooldown{
		clk:     clockOr(cfg.Clock),
		backoff: cfg.Backoff,
		quota:   cfg.QuotaCooldown,
		auth:    cfg.AuthCooldown,
		m:       make(map[Key]*cooldownEntry),
	}
}

// Record notes a failure of class for key, with the provider's retry-after
// (zero when it named none). It returns the time the affected scope is cooled
// down until, and false when class sets no cooldown.
func (c *Cooldown) Record(key Key, class Class, retryAfter time.Duration) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clk.Now()
	var (
		scope = key
		d     time.Duration
	)
	switch class {
	case ClassQuota:
		d = c.quota
	case ClassAuth:
		scope, d = key.account(), c.auth
	case ClassTransient, ClassUnknown:
		e := c.entryLocked(scope)
		e.streak++
		d = c.backoff.Delay(e.streak)
	default:
		return time.Time{}, false
	}
	if retryAfter > 0 {
		d = retryAfter
	}
	e := c.entryLocked(scope)
	if until := now.Add(d); until.After(e.until) {
		e.until, e.class = until, class
	}
	return e.until, true
}

// Set cools key down until the given time for class, for a reset the provider
// reported out of band (for example a quota window's ResetsAt). It never
// shortens a cooldown already in force.
func (c *Cooldown) Set(key Key, class Class, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entryLocked(key)
	if until.After(e.until) {
		e.until, e.class = until, class
	}
}

// Success notes that a call for key succeeded. It ends the cooldown of key
// and of its account, and resets key's transient backoff. A cooldown of the
// whole resource is left alone: one account's success says nothing about the
// others.
func (c *Cooldown) Success(key Key) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
	delete(c.m, key.account())
}

// Clear drops key's own cooldown and backoff, whatever their class.
func (c *Cooldown) Clear(key Key) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

// Until returns when key may next be tried and the class that cooled it down,
// looking at key, its account and its resource and taking the latest. It
// returns false when none of them is cooling down.
func (c *Cooldown) Until(key Key) (time.Time, Class, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clk.Now()
	var (
		until time.Time
		class Class
		found bool
	)
	for _, k := range [...]Key{key, key.account(), key.resource()} {
		e := c.m[k]
		if e == nil {
			continue
		}
		if !e.until.After(now) {
			if e.streak == 0 {
				delete(c.m, k)
			}
			continue
		}
		if !found || e.until.After(until) {
			until, class, found = e.until, e.class, true
		}
	}
	return until, class, found
}

// RetryAfter returns how long until key may next be tried, zero when it may
// be tried now.
func (c *Cooldown) RetryAfter(key Key) time.Duration {
	until, _, ok := c.Until(key)
	if !ok {
		return 0
	}
	return max(until.Sub(c.clk.Now()), 0)
}

func (c *Cooldown) entryLocked(k Key) *cooldownEntry {
	e := c.m[k]
	if e == nil {
		e = &cooldownEntry{}
		c.m[k] = e
	}
	return e
}
