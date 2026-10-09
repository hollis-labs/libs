package messagingtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh/messaging"
	"github.com/hollis-labs/substrate/mesh/messaging/memstore"
	"github.com/hollis-labs/substrate/mesh/messaging/messagingtest"
)

// delayedSubscription models a store that needs time to register its live
// subscription. Returning from Subscribe, rather than elapsed parent time,
// guarantees that subsequent sends can reach the recipient.
type delayedSubscription struct{ *memstore.Store }

func (s delayedSubscription) Subscribe(ctx context.Context, to messaging.Address, f messaging.Filter) (<-chan messaging.Envelope, error) {
	if len(f.Kind) == 1 && f.Kind[0] == messaging.MsgKindRequest {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Store.Subscribe(ctx, to, f)
}

func TestContractWaitsForRequestSubscription(t *testing.T) {
	messagingtest.RunContract(t, func(t *testing.T) messaging.Store { return delayedSubscription{memstore.New()} })
}
