package chattest

import (
	"fmt"
	"sort"
	"sync"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
)

// Clock is a manual chat.Clock. Time moves only when Advance is called, and
// the functions that fall due run inside Advance, in order, before it returns.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	nextID int
	timers map[int]*timer
}

type timer struct {
	at time.Time
	f  func()
}

var _ chat.Clock = (*Clock)(nil)

// NewClock returns a Clock set to a fixed instant.
func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), timers: map[int]*timer{}}
}

// Now implements chat.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc implements chat.Clock. f runs from Advance.
func (c *Clock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	c.timers[id] = &timer{at: c.now.Add(d), f: f}
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, ok := c.timers[id]
		delete(c.timers, id)
		return ok
	}
}

// Advance moves the clock forward by d and runs every function that falls
// due, earliest first.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	for {
		ids := make([]int, 0, len(c.timers))
		for id, t := range c.timers {
			if !t.at.After(target) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			break
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := c.timers[ids[i]], c.timers[ids[j]]
			if a.at.Equal(b.at) {
				return ids[i] < ids[j]
			}
			return a.at.Before(b.at)
		})
		t := c.timers[ids[0]]
		delete(c.timers, ids[0])
		if t.at.After(c.now) {
			c.now = t.at
		}
		c.mu.Unlock()
		t.f()
		c.mu.Lock()
	}
	c.now = target
	c.mu.Unlock()
}

// Pending reports how many functions are waiting to run.
func (c *Clock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// IDs returns a chat.Config.NewID that counts per prefix: conv_1, run_1,
// run_2 and so on.
func IDs() func(prefix string) string {
	var mu sync.Mutex
	n := map[string]int{}
	return func(prefix string) string {
		mu.Lock()
		defer mu.Unlock()
		n[prefix]++
		return fmt.Sprintf("%s_%d", prefix, n[prefix])
	}
}
