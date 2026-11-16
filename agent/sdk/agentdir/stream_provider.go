package agentdir

import (
	"context"
	"fmt"

	"github.com/hollis-labs/libs/llm/core/llmcontracts"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
)

// TapProvider forwards an authorized provider stream into the SDK and sends
// each text delta to the host's chatstream writer before forwarding it. The
// host constructs this around its selected provider in SDKFactory.StreamPorts.
// It does not invoke the provider or bypass the SDK's admission checks.
type TapProvider struct {
	Provider llmcontracts.Provider
	Delta    func(string) error
}

func (p TapProvider) Capabilities() llmtypes.ProviderCapabilities {
	return p.Provider.Capabilities()
}

func (p TapProvider) Complete(ctx context.Context, req llmtypes.ChatRequest) (string, error) {
	return p.Provider.Complete(ctx, req)
}

func (p TapProvider) StreamChat(ctx context.Context, req llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	if p.Provider == nil || p.Delta == nil {
		return nil, fmt.Errorf("agentdir: provider and delta sink required")
	}
	stream, err := p.Provider.StreamChat(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan llmtypes.StreamEvent)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-stream:
				if !ok {
					return
				}
				if event.Type == llmtypes.EventDelta && event.Content != "" {
					if err := p.Delta(event.Content); err != nil {
						select {
						case out <- llmtypes.StreamEvent{Type: llmtypes.EventError, Error: err.Error()}:
						case <-ctx.Done():
						}
						return
					}
				}
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
