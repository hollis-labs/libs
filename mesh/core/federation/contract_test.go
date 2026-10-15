package federation

import (
	"testing"

	gomsg "github.com/hollis-labs/substrate/mesh/messaging"
	"github.com/hollis-labs/substrate/mesh/messaging/messagingtest"
)

// The federation hop is a go-messaging Store, so it runs the same contract every
// Store does: Dial to a Server over a memstore, through real mutual TLS. The
// contract's addresses use the authority "test", which the server homes and the
// peer is registered for.
func contractFactory(ops OpSet) messagingtest.Factory {
	return func(t *testing.T) gomsg.Store {
		client := validIdentity(t)
		h := startHop(t, ops, []string{"test"}, []PeerConfig{peer("contract", client, "test")}, withSharedAuthorities())
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

// Outside shared-authority mode, a peer may not hold an authority this install
// homes: it could originate mail as any local address.
func TestNewServerRejectsPeerAuthorityOverlapUnlessMailboxOpsAreOn(t *testing.T) {
	client := validIdentity(t)
	reg, err := NewPeerRegistry([]PeerConfig{peer("p", client, "shared")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(newSpy(), MTLSPinnedResolver(reg), DefaultOpSet(), []string{"shared"}); err == nil {
		t.Fatal("default ops: a peer holding a local authority must be refused")
	}
	if _, err := NewServer(newSpy(), MTLSPinnedResolver(reg), DefaultOpSet(), []string{"other"}); err != nil {
		t.Fatalf("disjoint authorities: %v", err)
	}
	for _, op := range []Op{OpInbox, OpSubscribe} {
		wide, err := DefaultOpSet().Widen(op)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewServer(newSpy(), MTLSPinnedResolver(reg), wide, []string{"shared"}); err != nil {
			t.Fatalf("%s enabled is shared-authority mode and must be allowed: %v", op, err)
		}
	}
}
