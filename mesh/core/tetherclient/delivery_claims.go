package tether

import (
	"context"
	"net/http"
	"net/url"

	messaging "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/delivery"
)

// Durable delivery primitives — the claim/ack/nack cycle behind
// POST /messages/{id}/claim|ack|nack.
//
// These are the counterpart to Consume, not a replacement for it. Consume
// is one call that says "I have handled this"; the cycle here separates
// taking custody of a delivery from reporting what happened to it, so a
// crash in between is recoverable rather than silent. Reach for it when a
// host durably accepts a handoff before acknowledging it; reach for
// Consume when a single call is honest about what happened.
//
// The shape of a correct cycle:
//
//	env, lease, _, err := c.ClaimMessage(ctx, id, me, tether.ClaimOptions{})
//	// ... durably accept the work ...
//	_, _, err = c.AckMessage(ctx, id, me, lease, delivery.StageHostAccepted)
//	// ... do the work ...
//	_, _, err = c.AckMessage(ctx, id, me, lease, delivery.StageConsumed)
//
// and on failure:
//
//	_, _, err = c.NackMessage(ctx, id, me, lease, tether.NackOptions{Retryable: true})
//
// The lease returned by ClaimMessage is the bearer credential for the
// matching Ack/Nack — pass it back unmodified. The daemon also checks
// that the lease belongs to the {id} in the path, so a mismatched pair is
// rejected as a caller bug rather than silently acting on the wrong
// delivery.

// ClaimOptions tunes ClaimMessage. The zero value is valid and lets the
// daemon pick both fields.
type ClaimOptions struct {
	// Holder identifies the process taking custody. Empty means the
	// daemon defaults it to the asserted recipient URN.
	Holder string
	// LeaseSeconds requests a lease duration. Zero means the daemon's
	// default. The daemon clamps this to its own maximum, so the granted
	// duration ClaimMessage returns may be shorter than requested — use
	// that value, not this one.
	LeaseSeconds int
}

// NackOptions tunes NackMessage. The zero value dead-letters the delivery,
// because a Nack that says nothing is not a request to retry.
type NackOptions struct {
	// Retryable schedules another attempt. False dead-letters the
	// delivery, which is recoverable only through operator redrive.
	Retryable bool
	// Reason is recorded against the attempt. Worth setting: it is what
	// a later GET /messages/{id}/trace shows whoever is diagnosing this.
	Reason string
	// NextAttemptSeconds delays the retry. Zero means immediately
	// claimable again. Ignored when Retryable is false.
	NextAttemptSeconds int
}

type claimRequest struct {
	Holder       string `json:"holder,omitempty"`
	LeaseSeconds int    `json:"lease_seconds,omitempty"`
}

type claimResponse struct {
	Message   messaging.Envelope `json:"message"`
	Lease     delivery.LeaseRef  `json:"lease"`
	ExpiresIn int                `json:"lease_seconds"`
}

type ackRequest struct {
	Lease delivery.LeaseRef     `json:"lease"`
	Stage delivery.ReceiptStage `json:"stage"`
}

type nackRequest struct {
	Lease              delivery.LeaseRef `json:"lease"`
	Retryable          bool              `json:"retryable"`
	Error              string            `json:"error,omitempty"`
	NextAttemptSeconds int               `json:"next_attempt_seconds,omitempty"`
}

type receiptResponse struct {
	Delivery delivery.RecipientDelivery `json:"delivery"`
	Attempt  delivery.Attempt           `json:"attempt"`
}

// ClaimMessage takes custody of one message's delivery for recipient,
// returning the envelope, the lease to present to AckMessage/NackMessage,
// and the lease duration the daemon actually granted in seconds.
//
// The granted duration can be shorter than ClaimOptions.LeaseSeconds
// requested — the daemon clamps to its own maximum. Renew by finishing
// the cycle, not by re-claiming: a second claim against a live lease is
// refused.
func (c *Client) ClaimMessage(ctx context.Context, messageID string, recipient messaging.Address, opts ClaimOptions) (messaging.Envelope, delivery.LeaseRef, int, error) {
	q := url.Values{}
	q.Set("as", recipient.URN())
	path := withQuery("/messages/"+url.PathEscape(messageID)+"/claim", q)

	var out claimResponse
	body := claimRequest{Holder: opts.Holder, LeaseSeconds: opts.LeaseSeconds}
	if err := c.doJSON(ctx, http.MethodPost, path, body, http.StatusOK, &out); err != nil {
		return messaging.Envelope{}, delivery.LeaseRef{}, 0, mapStoreError(err)
	}
	return out.Message, out.Lease, out.ExpiresIn, nil
}

// AckMessage reports progress on a claimed delivery at stage — normally
// delivery.StageHostAccepted once custody is durable, then
// delivery.StageConsumed once the work is done.
//
// lease must be the one ClaimMessage returned for this same messageID.
func (c *Client) AckMessage(ctx context.Context, messageID string, recipient messaging.Address, lease delivery.LeaseRef, stage delivery.ReceiptStage) (delivery.RecipientDelivery, delivery.Attempt, error) {
	q := url.Values{}
	q.Set("as", recipient.URN())
	path := withQuery("/messages/"+url.PathEscape(messageID)+"/ack", q)

	var out receiptResponse
	if err := c.doJSON(ctx, http.MethodPost, path, ackRequest{Lease: lease, Stage: stage}, http.StatusOK, &out); err != nil {
		return delivery.RecipientDelivery{}, delivery.Attempt{}, mapStoreError(err)
	}
	return out.Delivery, out.Attempt, nil
}

// NackMessage declines or abandons a claimed delivery.
//
// A non-retryable Nack dead-letters the delivery. That is a SUCCESSFUL
// outcome of this call, not a failure, so it returns a nil error along
// with the resulting delivery and attempt — inspect
// delivery.RecipientDelivery to see the dead-lettered state rather than
// expecting an error. A dead-lettered delivery is recoverable only
// through operator redrive.
func (c *Client) NackMessage(ctx context.Context, messageID string, recipient messaging.Address, lease delivery.LeaseRef, opts NackOptions) (delivery.RecipientDelivery, delivery.Attempt, error) {
	q := url.Values{}
	q.Set("as", recipient.URN())
	path := withQuery("/messages/"+url.PathEscape(messageID)+"/nack", q)

	body := nackRequest{
		Lease:              lease,
		Retryable:          opts.Retryable,
		Error:              opts.Reason,
		NextAttemptSeconds: opts.NextAttemptSeconds,
	}
	var out receiptResponse
	if err := c.doJSON(ctx, http.MethodPost, path, body, http.StatusOK, &out); err != nil {
		return delivery.RecipientDelivery{}, delivery.Attempt{}, mapStoreError(err)
	}
	return out.Delivery, out.Attempt, nil
}
