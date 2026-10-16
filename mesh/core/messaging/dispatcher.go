package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// dispatcher is the canonical Dispatcher implementation, wrapping any Store.
type dispatcher struct {
	Store
}

// NewDispatcher returns a Dispatcher wrapping the given Store. The returned
// Dispatcher IS a Store (it embeds the underlying Store interface).
func NewDispatcher(s Store) Dispatcher {
	return &dispatcher{Store: s}
}

// Reply constructs and sends a response envelope to `parent`.
func (d *dispatcher) Reply(ctx context.Context, parent Envelope, payload json.RawMessage) (Envelope, error) {
	resp := Envelope{
		Kind:        MsgKindResponse,
		From:        parent.To,
		To:          parent.From,
		ThreadID:    parent.ThreadID,
		InReplyTo:   parent.ID,
		Payload:     payload,
		ContentType: "application/json",
	}
	return d.Send(ctx, resp)
}

// requestError applies Request's timeout contract to an error from the Store calls that come before the wait. When the
// caller's deadline expires while the Store is still subscribing or sending (a slow store, a busy host), the Store reports its
// own wrapped context error; the caller asked for a deadline and must get ErrRequestTimeout whichever step noticed it first.
// The original error stays in the chain, so errors.Is matches both. Any other error, and a cancellation, pass through.
func requestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrRequestTimeout, err)
	}
	return err
}

// Request sends an envelope as Kind=request and blocks until a matching
// response (InReplyTo=<request id>) arrives or ctx expires.
func (d *dispatcher) Request(ctx context.Context, env Envelope) (Envelope, error) {
	env.Kind = MsgKindRequest
	env.ID = "" // force Store to assign fresh ID

	// Subscribe BEFORE Send so we don't miss a fast response.
	// Subscribe as the requester (env.From): we receive responses addressed
	// to us, then filter by InReplyTo to match concurrent in-flight requests.
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()

	sub, err := d.Subscribe(subCtx, env.From, Filter{Kind: []Kind{MsgKindResponse}})
	if err != nil {
		return Envelope{}, requestError(ctx, err)
	}

	sent, err := d.Send(ctx, env)
	if err != nil {
		return Envelope{}, requestError(ctx, err)
	}

	for {
		select {
		case resp, ok := <-sub:
			if !ok {
				// Channel closed; ctx done.
				if ctx.Err() == context.DeadlineExceeded {
					return Envelope{}, ErrRequestTimeout
				}
				return Envelope{}, ctx.Err()
			}
			if resp.InReplyTo == sent.ID {
				return resp, nil
			}
			// Different response; ignore and keep waiting.
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return Envelope{}, ErrRequestTimeout
			}
			return Envelope{}, ctx.Err()
		}
	}
}
