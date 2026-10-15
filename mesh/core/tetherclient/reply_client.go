package tether

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const (
	CodeInterruptUnsupported  = "interrupt_unsupported"
	CodeTurnNotYetStarted     = "turn_not_yet_started"
	CodeTurnFeedUnavailable   = "turn_feed_unavailable"
	CodeReplyTargetNotSession = "reply_target_not_a_session"
	CodeReplyNotMailbox       = "reply_not_mailbox"
)

// ReplyOptions controls delivery to the session that published a routed message.
type ReplyOptions struct {
	Interrupt bool `json:"interrupt"`
	// IdempotencyKey is sent as Idempotency-Key, not a JSON field. Reusing the
	// same key and reply returns the earlier receipt without interrupting again.
	IdempotencyKey string `json:"-"`
}

// ReplyReceipt confirms acceptance, not delivery. Interrupt records the cancel
// result (including interrupt_timeout); Duplicate identifies an idempotent replay.
type ReplyReceipt struct {
	ReplyID         string `json:"reply_id"`
	ParentID        string `json:"parent_id"`
	State           string `json:"state"`
	TargetSessionID string `json:"target_session_id"`
	Interrupt       string `json:"interrupt,omitempty"`
	Duplicate       bool   `json:"duplicate,omitempty"`
}

// Reply queues body as the publishing session's next turn. Configure WithSelfURN
// or a bearer credential for caller identity. Refused replies return *APIError,
// preserving the daemon's status and code, and are not accepted. This method
// never retries: callers may reuse an IdempotencyKey for safe explicit retries.
func (c *Client) Reply(ctx context.Context, msgID, body string, opts ReplyOptions) (ReplyReceipt, error) {
	var out ReplyReceipt
	if msgID == "" {
		return out, errEmptyArg("message ID")
	}
	raw, err := json.Marshal(struct {
		Body      string `json:"body"`
		Interrupt bool   `json:"interrupt,omitempty"`
	}{body, opts.Interrupt})
	if err != nil {
		return out, err
	}
	path := withQuery("/messages/"+url.PathEscape(msgID)+"/reply", c.channelCaller())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", opts.IdempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return out, wrapIfUnreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return out, readAPIError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("decode reply receipt: %w", err)
	}
	return out, nil
}
