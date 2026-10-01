package tether

import (
	"context"
	"encoding/json"
	"net/http"

	messaging "github.com/hollis-labs/go-messaging"
)

// Notify urgencies (RFC 8030 Web Push vocabulary). An empty
// NotifyRequest.Urgency is "normal".
const (
	UrgencyVeryLow = "very-low"
	UrgencyLow     = "low"
	UrgencyNormal  = "normal"
	UrgencyHigh    = "high"
)

// Wake reasons: NotifyResult.WakeReason, a non-error account of a wake that
// did not deliver. None means the message was lost: it is stored, and its
// delivery is either retried by the daemon's wake sweep or, for
// already-handled, settled. A wake that was attempted and failed is
// reported in NotifyResult.WakeError instead.
const (
	// WakeReasonBusy: the session was mid-turn; retried shortly.
	WakeReasonBusy = "busy"
	// WakeReasonOffline: no live session to wake.
	WakeReasonOffline = "offline"
	// WakeReasonOfflineRace: the session stopped between resolution and the
	// wake.
	WakeReasonOfflineRace = "offline-race"
	// WakeReasonStaleGeneration: the recipient actor moved to a newer
	// session while the wake was in flight.
	WakeReasonStaleGeneration = "stale-generation"
	// WakeReasonClaimUnavailable: another attempt already holds the
	// delivery, so this one stood down rather than wake twice.
	WakeReasonClaimUnavailable = "claim-unavailable"
	// WakeReasonMarkerWriteFailed: the daemon could not record the
	// attempt's bookkeeping and released the delivery for retry.
	WakeReasonMarkerWriteFailed = "marker-write-failed"
	// WakeReasonAlreadyHandled: the recipient had already consumed or read
	// the message, so it was settled without a wake. Seen on retries.
	WakeReasonAlreadyHandled = "already-handled"
	// WakeReasonSettleFailed: settling an already-handled message failed;
	// it is retried.
	WakeReasonSettleFailed = "settle-failed"
	// WakeReasonSessionNotRunning: the recipient actor is bound to a
	// session that is not running, so there was nothing to wake.
	WakeReasonSessionNotRunning = "session-not-running"
)

// NotifyRequest is POST /messages/notify: a message envelope plus how to
// wake its recipient. From and To are required; an empty Kind is notice.
type NotifyRequest struct {
	Kind        messaging.Kind    `json:"kind,omitempty"`
	Channel     messaging.Channel `json:"channel,omitempty"`
	From        messaging.Address `json:"from"`
	To          messaging.Address `json:"to"`
	ThreadID    string            `json:"thread_id,omitempty"`
	InReplyTo   string            `json:"in_reply_to,omitempty"`
	Payload     json.RawMessage   `json:"payload,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Urgency is one of the Urgency constants; empty means normal.
	Urgency string `json:"urgency,omitempty"`
	// SessionID names the session to wake. It must be running and be the
	// recipient session or a session of the recipient actor; empty lets the
	// daemon resolve one.
	SessionID string `json:"session_id,omitempty"`
	// Wake set to false stores the message without waking anyone; nil
	// means wake.
	Wake *bool `json:"wake,omitempty"`
	// WakeText replaces the daemon's mailbox-reminder turn text.
	WakeText string `json:"wake_text,omitempty"`
}

// NotifyResult is POST /messages/notify's answer. WakeDelivered means the
// reminder turn was submitted to the session, not that the message was read.
type NotifyResult struct {
	Message       messaging.Envelope `json:"message"`
	UnreadCount   int                `json:"unread_count"`
	WakeAttempted bool               `json:"wake_attempted"`
	WakeDelivered bool               `json:"wake_delivered"`
	SessionID     string             `json:"session_id,omitempty"`
	// WakeError is an attempted wake that failed, such as a turn the
	// session refused.
	WakeError string `json:"wake_error,omitempty"`
	// WakeReason is one of the WakeReason constants when the wake did not
	// deliver for a reason that is not an error.
	WakeReason string `json:"wake_reason,omitempty"`
}

// Notify stores a message and wakes a live session of its recipient with a
// mailbox-reminder turn. The message is stored even when no wake happens;
// NotifyResult says why.
func (c *Client) Notify(ctx context.Context, req NotifyRequest) (NotifyResult, error) {
	var out NotifyResult
	if err := c.doJSON(ctx, http.MethodPost, "/messages/notify", req, http.StatusCreated, &out); err != nil {
		return NotifyResult{}, err
	}
	return out, nil
}
