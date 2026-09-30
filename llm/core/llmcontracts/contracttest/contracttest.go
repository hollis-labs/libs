package contracttest

import (
	"context"
	"testing"
	"time"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
	llmtypes "github.com/hollis-labs/go-llm-types"
)

// DefaultTimeout is the bound Run uses when its timeout argument is not
// positive.
const DefaultTimeout = 5 * time.Second

// tester is the slice of *testing.T the checks use. Keeping it small lets the
// package's own tests observe a failure without failing themselves.
type tester interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

// Run exercises the interface-level invariants every llmcontracts.Provider
// implementation must satisfy. newProvider is called once per subtest and
// must return a Provider wired to a fully deterministic double -- Run
// never touches a network or spawns a process itself.
//
// The invariants are:
//   - Capabilities does not panic.
//   - StreamChat returns either a non-nil error with a nil channel, or a
//     non-nil channel that delivers exactly one turn-terminal event (as
//     reported by llmtypes.IsTurnComplete) and then closes. (nil, nil) is a
//     violation, as is a non-nil channel alongside an error. A Provider that
//     always fails StreamChat synchronously (a Complete-only client) passes.
//   - Complete returns; its result and error are not inspected.
//
// timeout bounds each call, both as the context deadline handed to the
// Provider and as the wait for the Provider to honor it; a Provider that
// obeys its context is given a further half timeout to return after the
// deadline fires. A non-positive timeout selects DefaultTimeout.
func Run(t *testing.T, newProvider func(t *testing.T) llmcontracts.Provider, req llmtypes.ChatRequest, timeout time.Duration) {
	t.Helper()
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	t.Run("Capabilities_NoPanic", func(t *testing.T) {
		checkCapabilities(t, newProvider(t))
	})
	t.Run("StreamChat_ChannelProtocol", func(t *testing.T) {
		checkStreamChat(t, newProvider(t), req, timeout)
	})
	t.Run("Complete_ReturnsWithinTimeout", func(t *testing.T) {
		checkComplete(t, newProvider(t), req, timeout)
	})
}

// grace is how long past its context deadline a Provider may take to return.
func grace(timeout time.Duration) time.Duration { return timeout / 2 }

func checkCapabilities(t tester, p llmcontracts.Provider) {
	t.Helper()
	_ = p.Capabilities()
}

func checkStreamChat(t tester, p llmcontracts.Provider, req llmtypes.ChatRequest, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel() // also releases a producer blocked sending to an abandoned channel

	ch, err := p.StreamChat(ctx, req)
	if err != nil {
		if ch != nil {
			t.Fatalf("StreamChat returned a non-nil channel alongside a non-nil error: %v", err)
		}
		return // unsupported / failed synchronously -- a valid outcome
	}
	if ch == nil {
		t.Fatal("StreamChat returned (nil, nil): a caller ranging over this channel would hang forever")
	}

	limit := time.NewTimer(timeout + grace(timeout))
	defer limit.Stop()

	terminal := 0
	var last llmtypes.StreamEvent
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if terminal != 1 {
					t.Fatalf("channel closed after %d turn-terminal events, want exactly 1 (last event: %+v)", terminal, last)
				}
				return
			}
			last = ev
			if llmtypes.IsTurnComplete(ev) {
				terminal++
			}
		case <-limit.C:
			t.Fatalf("StreamChat channel did not close within %s -- possible goroutine or process leak (turn-terminal events so far: %d)", timeout, terminal)
		}
	}
}

func checkComplete(t tester, p llmcontracts.Provider, req llmtypes.ChatRequest, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Complete(ctx, req)
	}()

	limit := time.NewTimer(timeout + grace(timeout))
	defer limit.Stop()
	select {
	case <-done:
	case <-limit.C:
		t.Fatalf("Complete did not return within %s", timeout)
	}
}
