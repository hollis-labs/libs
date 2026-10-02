package tether

import (
	"context"
	"net/url"
	"time"
)

// ReplyState is the daemon's durable delivery state. Unknown future values
// remain readable. Only delivered and undeliverable are terminal today.
type ReplyState string

const (
	ReplyPending       ReplyState = "pending"
	ReplyQueued        ReplyState = "queued"
	ReplyDelivering    ReplyState = "delivering"
	ReplyDelivered     ReplyState = "delivered"
	ReplyUndeliverable ReplyState = "undeliverable"

	ReplyReasonHandedOff                     = "handed_off"
	ReplyReasonTurnFailed                    = "turn_failed"
	ReplyReasonSessionEndedNoBinding         = "session_ended_no_binding"
	ReplyReasonBoundSessionNotRunning        = "bound_session_not_running"
	ReplyReasonPullOnlyBinding               = "pull_only_binding"
	ReplyReasonResolveFailed                 = "resolve_failed"
	ReplyReasonSubmitFailed                  = "submit_failed"
	ReplyReasonNoTurnFeed                    = "no_turn_feed"
	ReplyReasonDaemonRestartedDuringDelivery = "daemon_restarted_during_delivery"
	ReplyReasonInterruptUnconfirmed          = "interrupt_unconfirmed"
	ReplyReasonBodyPurged                    = "body_purged"
	ReplyReasonWaitingForIdle                = "waiting_for_idle"
)

// ReplyDelivery mirrors the daemon's delivery record, without the reply body.
// A delivered reply with reason turn_failed was injected but its turn failed;
// it is terminal and must not be retried as a new reply.
type ReplyDelivery struct {
	ReplyID              string     `json:"reply_id"`
	ParentID             string     `json:"parent_id"`
	State                ReplyState `json:"state"`
	Reason               string     `json:"reason,omitempty"`
	Detail               string     `json:"detail,omitempty"`
	OriginalSessionID    string     `json:"original_session_id"`
	TargetSessionID      string     `json:"target_session_id"`
	DeliveredToSessionID string     `json:"delivered_to_session_id,omitempty"`
	InterruptRequested   bool       `json:"interrupt_requested"`
	Attempts             int        `json:"attempts"`
	NextAttemptAt        *time.Time `json:"next_attempt_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	SettledAt            *time.Time `json:"settled_at,omitempty"`
}

// ReplyDelivery reads a reply's current delivery record. An unknown reply
// returns *APIError with status 404; this method does not poll automatically.
func (c *Client) ReplyDelivery(ctx context.Context, replyID string) (ReplyDelivery, error) {
	var out ReplyDelivery
	if replyID == "" {
		return out, errEmptyArg("reply ID")
	}
	err := c.getJSON(ctx, withQuery("/messages/"+url.PathEscape(replyID)+"/delivery", c.channelCaller()), &out)
	return out, err
}
