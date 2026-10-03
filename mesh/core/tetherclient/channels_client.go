package tether

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	gomsg "github.com/hollis-labs/go-messaging"
)

// Channel's name is its stable handle; Address is derived by the daemon.
type Channel struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// ChannelMessage carries the durable replay cursor alongside the envelope.
type ChannelMessage struct {
	gomsg.Envelope
	Seq      int64      `json:"seq"`
	Purged   bool       `json:"purged,omitempty"`
	PurgedAt *time.Time `json:"purged_at,omitempty"`
}

type ChannelMessagesOptions struct {
	Since int64
	Limit int
	// Last requests the latest N publications, oldest first. It cannot be
	// combined with a nonzero Since or Limit.
	Last int
}

type ChannelMessagesResponse struct {
	Channel
	Messages  []ChannelMessage `json:"messages"`
	NextSince int64            `json:"next_since"`
}

func (c *Client) channelCaller() url.Values {
	q := make(url.Values)
	if c.selfURN != "" {
		q.Set("as", c.selfURN)
	}
	return q
}

// ListChannels lists published channels. Configure WithSelfURN or a bearer
// credential to identify the caller; channel membership is not required.
func (c *Client) ListChannels(ctx context.Context) ([]Channel, error) {
	var out struct {
		Channels []Channel `json:"channels"`
	}
	err := c.getJSON(ctx, withQuery("/channels", c.channelCaller()), &out)
	return out.Channels, err
}

// ChannelMessages reads history after Since, exclusively. Limit zero uses the
// daemon default. NextSince can be passed to the next history or subscribe call.
func (c *Client) ChannelMessages(ctx context.Context, name string, opts ChannelMessagesOptions) (ChannelMessagesResponse, error) {
	var out ChannelMessagesResponse
	if name == "" {
		return out, errEmptyArg("channel name")
	}
	if opts.Since < 0 || opts.Limit < 0 || opts.Last < 0 {
		return out, fmt.Errorf("tether: channel since, limit and last must be nonnegative")
	}
	q := c.channelCaller()
	if opts.Last != 0 {
		if opts.Since != 0 || opts.Limit != 0 {
			return out, fmt.Errorf("tether: channel last cannot be combined with since or limit")
		}
		q.Set("last", strconv.Itoa(opts.Last))
	} else {
		q.Set("since", strconv.FormatInt(opts.Since, 10))
	}
	if opts.Limit != 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	err := c.getJSON(ctx, withQuery("/channels/"+url.PathEscape(name)+"/messages", q), &out)
	return out, err
}

// SubscribeChannel streams publications until ctx is cancelled or the server
// disconnects. Nil since starts live; a pointer to zero replays all history.
// Reconnect explicitly with the last received Seq to resume. Initial HTTP
// errors are returned directly; stream decoding/transport errors use errs.
// The subscription uses caller cancellation rather than HTTP client Timeout.
func (c *Client) SubscribeChannel(ctx context.Context, name string, since *int64) (<-chan ChannelMessage, <-chan error, error) {
	if name == "" {
		return nil, nil, errEmptyArg("channel name")
	}
	q := c.channelCaller()
	if since != nil {
		if *since < 0 {
			return nil, nil, fmt.Errorf("tether: channel since must be nonnegative")
		}
		q.Set("since", strconv.FormatInt(*since, 10))
	}
	resp, err := c.doStream(ctx, "/channels/"+url.PathEscape(name)+"/subscribe", q)
	if err != nil {
		return nil, nil, err
	}
	events := make(chan ChannelMessage, 16)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		var data strings.Builder
		event := ""
		flush := func() error {
			defer func() { data.Reset(); event = "" }()
			if data.Len() == 0 || (event != "" && event != "message") {
				return nil
			}
			var msg ChannelMessage
			if err := json.Unmarshal([]byte(data.String()), &msg); err != nil {
				return fmt.Errorf("decode channel message: %w", err)
			}
			select {
			case events <- msg:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if err := flush(); err != nil {
					if ctx.Err() == nil {
						errs <- err
					}
					return
				}
				continue
			}
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			errs <- fmt.Errorf("read channel stream: %w", err)
		}
		// An incomplete frame at EOF is discarded, so reconnecting from the
		// last delivered cursor replays the unfinished publication.
	}()
	return events, errs, nil
}
