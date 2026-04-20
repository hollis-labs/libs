package agentmux

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func (c *Client) streamEvents(ctx context.Context, opts StreamEventsOptions, out chan<- StreamEvent) error {
	q := url.Values{}
	if opts.SinceSeq > 0 {
		q.Set("since_seq", strconv.FormatInt(opts.SinceSeq, 10))
	}
	if opts.SessionID != "" {
		q.Set("session_id", opts.SessionID)
	}
	for _, scope := range opts.Scopes {
		q.Add("scope", scope)
	}
	resp, err := c.doStream(ctx, "/events/stream", q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var ev StreamEvent
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if ev.ID != 0 || ev.Event != "" || ev.Scope != "" || ev.PayloadJSON != "" {
				select {
				case out <- ev:
				case <-ctx.Done():
					return ctx.Err()
				}
				ev = StreamEvent{}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "id":
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return fmt.Errorf("parse SSE id %q: %w", value, err)
			}
			ev.ID = id
		case "event":
			ev.Event = value
		case "data":
			var data struct {
				Scope       string `json:"scope"`
				SessionID   string `json:"session_id,omitempty"`
				PayloadJSON string `json:"payload_json,omitempty"`
			}
			if err := json.Unmarshal([]byte(value), &data); err != nil {
				return fmt.Errorf("parse SSE data: %w", err)
			}
			ev.Scope = data.Scope
			ev.SessionID = data.SessionID
			ev.PayloadJSON = data.PayloadJSON
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}
