package federation

import (
	"testing"

	gomsg "github.com/hollis-labs/go-messaging"
	"github.com/hollis-labs/go-messaging/messagingtest"
)

// The federation hop is a go-messaging Store, so it runs the same contract every
// Store does: Dial to a Server over a memstore, through real mutual TLS. The
// contract's addresses use the authority "test", which the server homes and the
// peer is registered for.
func contractFactory(ops OpSet) messagingtest.Factory {
	return func(t *testing.T) gomsg.Store {
		client := validIdentity(t)
		h := startHop(t, ops, []string{"test"}, []PeerConfig{peer("contract", client, "test")})
		return h.dial(client, WithOpSet(ops))
	}
}

// Under DefaultOpSet the hop carries no Inbox or Subscribe, so the sub-tests that
// need them are skipped (and reported as skipped, not silently absent).
func TestDefaultOpSetPassesTheStoreContractWithoutInboxAndSubscribe(t *testing.T) {
	messagingtest.RunContract(t, contractFactory(DefaultOpSet()), messagingtest.Without("Inbox", "Subscribe"))
}

// A widened OpSet carries everything, and the whole contract passes.
func TestWidenedOpSetPassesTheWholeStoreContract(t *testing.T) {
	wide, err := DefaultOpSet().Widen(OpInbox, OpSubscribe)
	if err != nil {
		t.Fatal(err)
	}
	messagingtest.RunContract(t, contractFactory(wide))
}
