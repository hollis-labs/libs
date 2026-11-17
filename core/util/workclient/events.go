package workclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Head is the sequence of the service's newest change event (from /healthz).
func (c *Client) Head(ctx context.Context) (int64, error) {
	var h struct {
		Head *int64 `json:"events_head"`
	}
	if err := c.do(ctx, "GET", "/healthz", nil, nil, &h); err != nil {
		return 0, err
	}
	if h.Head == nil {
		return 0, &wrapped{kind: ErrBadAnswer, cause: errors.New("the health answer has no events_head")}
	}
	return *h.Head, nil
}

// EventStream opens the change stream for the owner namespace: Server-Sent Events after
// the sequence since, each with id (its sequence), event (its kind) and data (an Event as
// JSON). With since "" it starts at the service's current head, so a new subscriber is not
// sent the whole history. from is the sequence it starts after either way. The caller
// closes the stream; NewEventReader reads it.
func (c *Client) EventStream(ctx context.Context, since string) (stream io.ReadCloser, from string, err error) {
	if since == "" {
		head, err := c.Head(ctx)
		if err != nil {
			return nil, "", err
		}
		since = strconv.FormatInt(head, 10)
	}
	req, err := c.newRequest(ctx, "GET", "/v1/events", url.Values{"since": {since}}, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, "", &wrapped{kind: ErrUnreachable, cause: err}
	}
	if resp.StatusCode != 200 {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, "", answerError(resp.StatusCode, raw)
	}
	return resp.Body, since, nil
}

// EventReader reads Events from a change stream.
type EventReader struct {
	sc *bufio.Scanner
}

// NewEventReader reads the stream r (from EventStream).
func NewEventReader(r io.Reader) *EventReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	return &EventReader{sc: sc}
}

// Next is the next event. Comments, keep-alives and frames without data are skipped. At the
// end of the stream it returns io.EOF; a frame whose data is not an Event is ErrBadAnswer.
func (r *EventReader) Next() (Event, error) {
	var data []string
	for r.sc.Scan() {
		line := r.sc.Text()
		if line == "" {
			if len(data) == 0 {
				continue
			}
			var ev Event
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &ev); err != nil {
				return Event{}, &wrapped{kind: ErrBadAnswer, cause: err}
			}
			return ev, nil
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	if err := r.sc.Err(); err != nil {
		return Event{}, err
	}
	return Event{}, io.EOF
}
