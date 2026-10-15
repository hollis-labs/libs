package guard_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hollis-labs/substrate/llm-core/guard"
)

type stoppedClock struct{}

func (stoppedClock) Now() time.Time { return time.Unix(1_700_000_000, 0) }

// An adapter wraps each provider call in Guard.Do and returns a classified
// error, so a 429 cools down only that account and model, and a 400 cools
// down nothing.
func ExampleGuard_Do() {
	g := guard.New(guard.Config{
		Breaker: guard.BreakerConfig{Threshold: 3, Cooldown: 30 * time.Second},
		Clock:   stoppedClock{}, // a fixed time keeps the printed wait exact
	})
	key := guard.Key{Resource: "provider-a", Account: "team-key", Model: "model-x"}

	call := func(status int) func(context.Context) error {
		return func(context.Context) error {
			if status == http.StatusOK {
				return nil
			}
			h := http.Header{"Retry-After": []string{"20"}}
			return guard.HTTPError(status, h, fmt.Errorf("provider returned %d", status))
		}
	}

	fmt.Println(g.Do(context.Background(), key, call(http.StatusBadRequest)))
	fmt.Println(g.Check(key).Allowed)

	_ = g.Do(context.Background(), key, call(http.StatusTooManyRequests))
	err := g.Do(context.Background(), key, call(http.StatusOK))
	fmt.Println(errors.Is(err, guard.ErrRefused), err)
	// Output:
	// guard: request (status 400): provider returned 400
	// true
	// true guard: refused (cooldown, quota, retry after 20s)
}

// CircuitBreaker can be used on its own. Admit returns an Admission whose
// result only counts if the breaker has not changed state since.
func ExampleCircuitBreaker_Admit() {
	cb := guard.NewCircuitBreaker(guard.BreakerConfig{Threshold: 1, Cooldown: time.Millisecond})
	cb.RecordFailure()
	time.Sleep(2 * time.Millisecond)

	probe, ok := cb.Admit()
	_, other := cb.Admit()
	fmt.Println(ok, probe.Probe(), other)

	probe.Success()
	fmt.Println(cb.State())
	// Output:
	// true true false
	// closed
}
