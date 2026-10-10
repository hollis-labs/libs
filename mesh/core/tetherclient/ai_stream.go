package tether

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/hollis-labs/go-ssekit"
)

func (c *Client) AIChatStream(ctx context.Context, req ChatRequest) (<-chan AIStreamEvent, <-chan error, error) {
	resp, err := c.doJSONStream(ctx, http.MethodPost, "/ai/chat/stream", req)
	if err != nil {
		return nil, nil, err
	}

	eventsCh := make(chan AIStreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		defer close(eventsCh)
		defer close(errCh)
		defer func() { _ = resp.Body.Close() }()

		for event, err := range ssekit.Read(resp.Body) {
			if err != nil {
				if ctx.Err() == nil {
					errCh <- fmt.Errorf("read ai chat stream: %w", err)
				}
				return
			}
			if len(event.Data) == 0 {
				continue
			}
			var ev AIStreamEvent
			if err := decodeJSON(bytes.NewReader(event.Data), &ev); err != nil {
				if ctx.Err() == nil {
					errCh <- fmt.Errorf("decode ai chat stream event: %w", err)
				}
				return
			}
			select {
			case eventsCh <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return eventsCh, errCh, nil
}
