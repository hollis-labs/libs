package contracttest

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
	llmtypes "github.com/hollis-labs/go-llm-types"
)

// stub is a configurable Provider double.
type stub struct {
	stream   func(ctx context.Context) (<-chan llmtypes.StreamEvent, error)
	complete func(ctx context.Context) (string, error)
	caps     func() llmtypes.ProviderCapabilities
}

func (s stub) StreamChat(ctx context.Context, _ llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	return s.stream(ctx)
}

func (s stub) Complete(ctx context.Context, _ llmtypes.ChatRequest) (string, error) {
	if s.complete == nil {
		return "ok", nil
	}
	return s.complete(ctx)
}

func (s stub) Capabilities() llmtypes.ProviderCapabilities {
	if s.caps == nil {
		return llmtypes.ProviderCapabilities{}
	}
	return s.caps()
}

var _ llmcontracts.Provider = stub{}

func events(evs ...llmtypes.StreamEvent) func(context.Context) (<-chan llmtypes.StreamEvent, error) {
	return func(context.Context) (<-chan llmtypes.StreamEvent, error) {
		ch := make(chan llmtypes.StreamEvent, len(evs))
		for _, ev := range evs {
			ch <- ev
		}
		close(ch)
		return ch, nil
	}
}

var (
	delta = llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "hi"}
	done  = llmtypes.StreamEvent{Type: llmtypes.EventDone}
	fail  = llmtypes.StreamEvent{Type: llmtypes.EventError, Error: "boom"}
)

// fakeT records a fatal and, like *testing.T, stops the calling goroutine.
type fakeT struct {
	failed bool
	msg    string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatal(args ...any) {
	f.failed = true
	f.msg = fmt.Sprint(args...)
	runtime.Goexit()
}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed = true
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// observe runs check on its own goroutine so a fatal's Goexit is contained.
func observe(check func(t tester)) *fakeT {
	f := &fakeT{}
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		check(f)
	}()
	<-fin
	return f
}

const short = 100 * time.Millisecond

func TestRun_CorrectProviderPasses(t *testing.T) {
	Run(t, func(*testing.T) llmcontracts.Provider {
		return stub{stream: events(delta, delta, done)}
	}, llmtypes.ChatRequest{}, short)
}

func TestRun_CorrectProviderWithErrorTerminalPasses(t *testing.T) {
	Run(t, func(*testing.T) llmcontracts.Provider {
		return stub{stream: events(delta, fail)}
	}, llmtypes.ChatRequest{}, short)
}

func TestRun_CompleteOnlyProviderPasses(t *testing.T) {
	Run(t, func(*testing.T) llmcontracts.Provider {
		return stub{stream: func(context.Context) (<-chan llmtypes.StreamEvent, error) {
			return nil, context.Canceled
		}}
	}, llmtypes.ChatRequest{}, short)
}

func TestRun_NonPositiveTimeoutUsesDefault(t *testing.T) {
	Run(t, func(*testing.T) llmcontracts.Provider {
		return stub{stream: events(done)}
	}, llmtypes.ChatRequest{}, 0)
}

func TestRun_CallsFactoryPerSubtest(t *testing.T) {
	n := 0
	Run(t, func(*testing.T) llmcontracts.Provider {
		n++
		return stub{stream: events(done)}
	}, llmtypes.ChatRequest{}, short)
	if n != 3 {
		t.Fatalf("newProvider called %d times, want 3", n)
	}
}

func TestCheckStreamChat_Violations(t *testing.T) {
	tests := []struct {
		name    string
		stream  func(context.Context) (<-chan llmtypes.StreamEvent, error)
		wantMsg string
	}{
		{"double terminal", events(delta, done, done), "2 turn-terminal"},
		{"done then error", events(done, fail), "2 turn-terminal"},
		{"no terminal", events(delta, delta), "0 turn-terminal"},
		{"empty closed channel", events(), "0 turn-terminal"},
		{"nil channel nil error", func(context.Context) (<-chan llmtypes.StreamEvent, error) {
			return nil, nil
		}, "(nil, nil)"},
		{"channel with error", func(context.Context) (<-chan llmtypes.StreamEvent, error) {
			return make(chan llmtypes.StreamEvent), context.Canceled
		}, "non-nil channel alongside"},
		{"never closes", func(context.Context) (<-chan llmtypes.StreamEvent, error) {
			ch := make(chan llmtypes.StreamEvent, 1)
			ch <- done
			return ch, nil // terminal delivered but channel left open
		}, "did not close"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := observe(func(ft tester) {
				checkStreamChat(ft, stub{stream: tc.stream}, llmtypes.ChatRequest{}, short)
			})
			if !f.failed {
				t.Fatal("check passed a violating provider")
			}
			if !strings.Contains(f.msg, tc.wantMsg) {
				t.Fatalf("failure %q does not mention %q", f.msg, tc.wantMsg)
			}
		})
	}
}

func TestCheckStreamChat_HonorsContextAtDeadline(t *testing.T) {
	// A well-behaved provider that blocks until its context deadline and then
	// terminates must not lose a race with Run's own timeout.
	s := stub{stream: func(ctx context.Context) (<-chan llmtypes.StreamEvent, error) {
		ch := make(chan llmtypes.StreamEvent)
		go func() {
			defer close(ch)
			<-ctx.Done()
			ch <- fail // receiver still draining within the grace window
		}()
		return ch, nil
	}}
	for i := 0; i < 5; i++ {
		f := observe(func(ft tester) { checkStreamChat(ft, s, llmtypes.ChatRequest{}, short) })
		if f.failed {
			t.Fatalf("ctx-honoring provider failed: %s", f.msg)
		}
	}
}

func TestCheckComplete_HangFails(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s := stub{complete: func(context.Context) (string, error) {
		<-release // ignores ctx
		return "", nil
	}}
	f := observe(func(ft tester) { checkComplete(ft, s, llmtypes.ChatRequest{}, short) })
	if !f.failed {
		t.Fatal("check passed a Complete that never returns")
	}
	if !strings.Contains(f.msg, "Complete did not return") {
		t.Fatalf("unexpected failure %q", f.msg)
	}
}

func TestCheckComplete_ContextHonoringPasses(t *testing.T) {
	s := stub{complete: func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	f := observe(func(ft tester) { checkComplete(ft, s, llmtypes.ChatRequest{}, short) })
	if f.failed {
		t.Fatalf("ctx-honoring Complete failed: %s", f.msg)
	}
}

func TestCheckCapabilities_PanicPropagates(t *testing.T) {
	// Run does not recover: a panicking Capabilities fails the subtest loudly.
	defer func() {
		if recover() == nil {
			t.Fatal("panic from Capabilities was swallowed")
		}
	}()
	checkCapabilities(&fakeT{}, stub{caps: func() llmtypes.ProviderCapabilities { panic("bad") }})
}
