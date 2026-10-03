package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/hollis-labs/substrate/mesh"
)

// DeliveryStore retains successfully accepted deliveries, not merely bound
// send keys. Hosts retain delegations until their result has been acknowledged.
// Returned values are detached and immutable; unknown keys return ErrNotFound.
type DeliveryStore interface {
	GetDelivery(context.Context, string) (Delivery, error)
	// DelegationState reads the host's current task state. A terminal or unknown
	// delegation cannot accept a new result, regardless of retained delivery.
	DelegationState(context.Context, string) (mesh.TaskState, error)
}

// ReplyResult delivers one result for a previously accepted delegation. The
// authenticated reporter must be its selected recipient. The original grant
// authorizes returning the result; no reverse may_message grant is required.
// The sender must honor DeliveryAtIdle, including across retries/restarts.
func (router *Router) ReplyResult(ctx context.Context, t Team, runID, delegateKey string, actor mesh.URN, body string) error {
	store, ok := router.Sender.(DeliveryStore)
	if !ok || router.Roster == nil || delegateKey == "" || body == "" {
		return fmt.Errorf("result: delivery store, roster, key and body required")
	}
	original, err := store.GetDelivery(ctx, delegateKey)
	if err != nil {
		return err
	}
	if original.Verb != mesh.Delegate || original.Route.RunID != runID || original.Route.TeamID != t.ID || original.Route.TeamVersion != t.Version {
		return ErrConflict
	}
	if actor != original.Recipient.Actor {
		return ErrDenied
	}
	key := stableID(runID, "delegate-result", delegateKey)
	delivery := Delivery{IdempotencyKey: key, From: actor, Recipient: clone(original.Route.Sender), Body: body, Verb: mesh.Reply, History: mesh.HistoryNone, InReplyTo: delegateKey, Delivery: mesh.DeliveryAtIdle, Route: clone(original.Route)}
	// Replay an accepted result before checking current liveness. The host may
	// have acknowledged the queue just before a process or run ended.
	prior, err := store.GetDelivery(ctx, key)
	if err == nil {
		if !reflect.DeepEqual(prior, delivery) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	roster, err := router.Roster.Snapshot(ctx, runID)
	if err != nil {
		return err
	}
	reporter, err := roster.member(actor)
	if err != nil {
		return fmt.Errorf("%w: assignee ended", ErrUnavailable)
	}
	if reporter.ID != original.Recipient.ID || reporter.SessionID != original.Recipient.SessionID {
		return ErrConflict
	}
	state, err := store.DelegationState(ctx, delegateKey)
	if err != nil {
		return err
	}
	if !state.Valid() || state.Terminal() {
		return ErrConflict
	}
	recipient, err := roster.member(original.From)
	if err != nil {
		return fmt.Errorf("%w: delegator ended", ErrUnavailable)
	}
	if recipient.ID != original.Route.Sender.ID || recipient.SessionID != original.Route.Sender.SessionID {
		return ErrConflict
	}
	encoded, err := json.Marshal(delivery)
	if err != nil {
		return err
	}
	if err = router.Sender.BindMessage(ctx, key, stableID(string(encoded))); err != nil {
		return err
	}
	return router.Sender.SendMessage(ctx, delivery)
}
