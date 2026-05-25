package tether

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
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
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var data strings.Builder
		flush := func() error {
			if data.Len() == 0 {
				return nil
			}
			var ev AIStreamEvent
			if err := decodeJSON(strings.NewReader(data.String()), &ev); err != nil {
				return fmt.Errorf("decode ai chat stream event: %w", err)
			}
			select {
			case eventsCh <- ev:
			case <-ctx.Done():
				return nil
			}
			data.Reset()
			return nil
		}

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if err := flush(); err != nil {
					errCh <- err
					return
				}
				continue
			}
			if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
				continue
			}
			if strings.HasPrefix(line, "data:") {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("read ai chat stream: %w", err)
			return
		}
		if err := flush(); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()
	return eventsCh, errCh, nil
}
