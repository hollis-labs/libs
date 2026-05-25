package tether

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
		if strings.TrimSpace(scope) != "" {
			q.Add("scope", scope)
		}
	}
	for _, kind := range opts.Kinds {
		if strings.TrimSpace(kind) != "" {
			q.Add("kind", kind)
		}
	}
	resp, err := c.doStream(ctx, "/events/stream", q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var (
		ev   StreamEvent
		data strings.Builder
	)
	flush := func() error {
		if ev.Seq == 0 && ev.Kind == "" && data.Len() == 0 {
			return nil
		}
		if data.Len() > 0 {
			var payload struct {
				Scope       string `json:"scope"`
				SessionID   string `json:"session_id,omitempty"`
				PayloadJSON string `json:"payload_json,omitempty"`
			}
			if err := json.Unmarshal([]byte(data.String()), &payload); err != nil {
				return fmt.Errorf("parse SSE data: %w", err)
			}
			ev.Scope = payload.Scope
			ev.SessionID = payload.SessionID
			ev.PayloadJSON = payload.PayloadJSON
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
		ev = StreamEvent{}
		data.Reset()
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
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
			ev.Seq = id
		case "event":
			ev.Kind = value
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	if err := flush(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
