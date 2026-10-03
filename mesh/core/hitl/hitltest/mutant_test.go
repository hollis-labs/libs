package hitltest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh/hitl"
	"github.com/hollis-labs/substrate/mesh/hitl/memstore"
)

type stop struct{}

// recorder collects failures instead of failing the real test.
type recorder struct{ failures []string }

func (r *recorder) Helper() {}
func (r *recorder) Errorf(f string, a ...any) {
	r.failures = append(r.failures, fmt.Sprintf(f, a...))
}
func (r *recorder) Fatalf(f string, a ...any) {
	r.Errorf(f, a...)
	panic(stop{})
}

func failures(a Adapter, sc Scenario) (fails []string) {
	rec := &recorder{}
	func() {
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(stop); !ok {
					panic(p)
				}
			}
		}()
		runScenario(rec, a, sc)
	}()
	return rec.failures
}

func scenario(t *testing.T, name string) Scenario {
	t.Helper()
	for _, s := range Scenarios() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no scenario %q", name)
	return Scenario{}
}

// override lets a test replace single Adapter methods of a working adapter.
type override struct {
	Adapter
	enqueue  func(context.Context, []byte) ([]byte, error)
	await    func(context.Context, []byte) ([]byte, error)
	get      func(context.Context, []byte) ([]byte, error)
	withdraw func(context.Context, []byte) ([]byte, error)
	resolve  func(context.Context, string, []byte) ([]byte, error)
}

func (o override) Enqueue(ctx context.Context, d []byte) ([]byte, error) {
	if o.enqueue != nil {
		return o.enqueue(ctx, d)
	}
	return o.Adapter.Enqueue(ctx, d)
}
func (o override) Get(ctx context.Context, d []byte) ([]byte, error) {
	if o.get != nil {
		return o.get(ctx, d)
	}
	return o.Adapter.Get(ctx, d)
}
func (o override) Await(ctx context.Context, d []byte) ([]byte, error) {
	if o.await != nil {
		return o.await(ctx, d)
	}
	return o.Adapter.Await(ctx, d)
}
func (o override) Withdraw(ctx context.Context, d []byte) ([]byte, error) {
	if o.withdraw != nil {
		return o.withdraw(ctx, d)
	}
	return o.Adapter.Withdraw(ctx, d)
}
func (o override) ParticipantResolve(ctx context.Context, id string, d []byte) ([]byte, error) {
	if o.resolve != nil {
		return o.resolve(ctx, id, d)
	}
	return o.Adapter.ParticipantResolve(ctx, id, d)
}

func good() Adapter {
	return NewServiceAdapter(hitl.NewService(memstore.New(), hitl.Options{}))
}

// The suite is only worth shipping if it can fail. Each subtest breaks one
// behavior and requires the named scenario to notice.
func TestConformanceRejectsBrokenAdapters(t *testing.T) {
	t.Run("the reference service passes every scenario", func(t *testing.T) {
		a := good()
		for _, sc := range Scenarios() {
			if f := failures(a, sc); len(f) > 0 {
				t.Errorf("%s: %v", sc.Name, f)
			}
		}
	})

	t.Run("an implementation that never enforces expiry", func(t *testing.T) {
		clock := func() time.Time { return time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC) }
		a := NewServiceAdapter(hitl.NewService(memstore.New(), hitl.Options{Clock: clock}))
		for _, name := range []string{"expiry-refuses-late-respond", "expiry-refuses-late-withdraw", "expiry-sweep"} {
			if len(failures(a, scenario(t, name))) == 0 {
				t.Errorf("%s passed against an adapter that ignores expires_at", name)
			}
		}
	})

	t.Run("an adapter that clamps wait_ms instead of rejecting", func(t *testing.T) {
		g := good()
		a := override{Adapter: g, await: func(ctx context.Context, d []byte) ([]byte, error) {
			var m map[string]any
			_ = json.Unmarshal(d, &m)
			m["wait_ms"] = 0
			b, _ := json.Marshal(m)
			return g.Await(ctx, b)
		}}
		if len(failures(a, scenario(t, "await-out-of-range"))) == 0 {
			t.Error("await-out-of-range passed although out-of-range waits were clamped")
		}
	})

	t.Run("an adapter that lets a later withdraw replace a resolution", func(t *testing.T) {
		g := good()
		a := override{Adapter: g, withdraw: func(ctx context.Context, d []byte) ([]byte, error) {
			out, err := g.Withdraw(ctx, d)
			if err == nil {
				return out, nil
			}
			// swallow the conflict and pretend the withdrawal worked
			var body map[string]any
			_ = json.Unmarshal(out, &body)
			if o, ok := body["terminal_outcome"].(map[string]any); ok {
				o["state"] = "canceled"
				o["cause"] = "caller_withdrawn"
				delete(o, "resolution")
				o["terminated_at"] = "2026-01-01T00:00:00Z"
				b, _ := json.Marshal(o)
				return b, nil
			}
			return out, err
		}}
		if len(failures(a, scenario(t, "resolve-then-withdraw"))) == 0 {
			t.Error("resolve-then-withdraw passed although the terminal outcome was overwritten")
		}
	})

	t.Run("an adapter whose error carries no wire body", func(t *testing.T) {
		g := good()
		a := override{Adapter: g, withdraw: func(ctx context.Context, d []byte) ([]byte, error) {
			out, err := g.Withdraw(ctx, d)
			if err != nil {
				return nil, err
			}
			return out, nil
		}}
		if len(failures(a, scenario(t, "resolve-then-withdraw"))) == 0 {
			t.Error("a bodyless error passed a typed-error scenario")
		}
	})

	t.Run("an adapter that violates the lifecycle", func(t *testing.T) {
		g := good()
		a := override{Adapter: g, get: func(ctx context.Context, d []byte) ([]byte, error) {
			out, err := g.Get(ctx, d)
			if err != nil {
				return out, err
			}
			var m map[string]any
			_ = json.Unmarshal(out, &m)
			if item, ok := m["item"].(map[string]any); ok && item["state"] == "canceled" {
				// report a canceled item as presented again
				item["state"] = "presented"
				delete(item, "terminal_outcome")
				m["item"] = item
				b, _ := json.Marshal(m)
				return b, nil
			}
			return out, nil
		}}
		if len(failures(a, scenario(t, "get-terminal"))) == 0 {
			t.Error("get-terminal passed although a terminal item was reopened")
		}
	})
}
