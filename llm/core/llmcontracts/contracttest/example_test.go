package contracttest_test

import (
	"context"
	"testing"
	"time"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
	"github.com/hollis-labs/go-llm-contracts/contracttest"
	llmtypes "github.com/hollis-labs/go-llm-types"
)

// fakeProvider is a deterministic double: it streams one delta and one done.
type fakeProvider struct{}

func (fakeProvider) StreamChat(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	ch := make(chan llmtypes.StreamEvent, 2)
	ch <- llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "hello"}
	ch <- llmtypes.StreamEvent{Type: llmtypes.EventDone}
	close(ch)
	return ch, nil
}

func (fakeProvider) Complete(context.Context, llmtypes.ChatRequest) (string, error) {
	return "hello", nil
}

func (fakeProvider) Capabilities() llmtypes.ProviderCapabilities {
	return llmtypes.ProviderCapabilities{}
}

// TestFakeProviderConforms runs the suite exactly as an adapter's own test
// file would.
func TestFakeProviderConforms(t *testing.T) {
	contracttest.Run(t, func(*testing.T) llmcontracts.Provider {
		return fakeProvider{}
	}, llmtypes.ChatRequest{}, 2*time.Second)
}

// ExampleRun shows the call an adapter's test makes. It is compile-checked
// only (no Output comment): Run needs the *testing.T of a real test, which
// TestFakeProviderConforms supplies.
func ExampleRun() {
	var t *testing.T // in real use: func TestProvider(t *testing.T)
	contracttest.Run(t, func(*testing.T) llmcontracts.Provider {
		return fakeProvider{}
	}, llmtypes.ChatRequest{}, 2*time.Second)
}
