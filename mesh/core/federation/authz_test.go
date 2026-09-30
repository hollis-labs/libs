package federation

import (
	"errors"
	"testing"

	gomsg "github.com/hollis-labs/go-messaging"
)

func authz(t *testing.T, local ...string) *authorizer {
	a, err := newAuthorizer(local)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var peerID = Identity{Label: "peer", Authorities: []string{"remote"}}

func TestNewAuthorizerRejectsAnInstallThatHomesNothing(t *testing.T) {
	if _, err := newAuthorizer(nil); err == nil {
		t.Error("no local authority")
	}
	if _, err := newAuthorizer([]string{"a", ""}); err == nil {
		t.Error("an empty local authority")
	}
}

func TestAuthorizeSend(t *testing.T) {
	a := authz(t, "local")
	tests := []struct {
		name string
		env  gomsg.Envelope
		want error
	}{
		{"allowed: from the peer's authority to a homed one", notice(agent("remote", "x"), agent("local", "y")), nil},
		{"relay: recipient is not homed here", notice(agent("remote", "x"), agent("elsewhere", "y")), errNotHomed},
		{"impersonation: sender is not the peer's authority", notice(agent("local", "x"), agent("local", "y")), errForbidden},
		{"impersonation of a third authority", notice(agent("third", "x"), agent("local", "y")), errForbidden},
		{"empty recipient authority", notice(agent("remote", "x"), agent("", "y")), errNotHomed},
		{"empty sender authority", notice(agent("", "x"), agent("local", "y")), errForbidden},
	}
	for _, tc := range tests {
		err := a.authorizeSend(peerID, tc.env)
		if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := a.authorizeSend(Identity{Label: "none"}, notice(agent("remote", "x"), agent("local", "y"))); !errors.Is(err, errForbidden) {
		t.Errorf("a peer with no authority may do nothing: %v", err)
	}
}

func TestAuthorizeEnvelopeAccess(t *testing.T) {
	a := authz(t, "local")
	tests := []struct {
		name string
		env  gomsg.Envelope
		want error
	}{
		{"peer is the sender, we home the recipient", notice(agent("remote", "x"), agent("local", "y")), nil},
		{"peer is the recipient, we home the sender", notice(agent("local", "x"), agent("remote", "y")), nil},
		{"peer is not a party", notice(agent("local", "x"), agent("local", "y")), errForbidden},
		{"we home neither party", notice(agent("remote", "x"), agent("elsewhere", "y")), errNotHomed},
	}
	for _, tc := range tests {
		err := a.authorizeEnvelopeAccess(OpGet, peerID, tc.env)
		if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestAuthorizeConsumeRequiresAnAddressOfTheEnvelope(t *testing.T) {
	a := authz(t, "local")
	env := notice(agent("remote", "x"), agent("local", "y"))
	if err := a.authorizeConsume(peerID, env, env.From); err != nil {
		t.Errorf("consuming for the envelope's sender, which the peer holds: %v", err)
	}
	if err := a.authorizeConsume(peerID, env, agent("remote", "somebody-else")); !errors.Is(err, errForbidden) {
		t.Errorf("a party must not consume on behalf of an unrelated recipient: %v", err)
	}
	if err := a.authorizeConsume(peerID, notice(agent("local", "x"), agent("local", "y")), agent("local", "y")); !errors.Is(err, errForbidden) {
		t.Errorf("a non-party must not consume at all: %v", err)
	}
}

// A caller may consume only for a recipient in an authority it holds: a peer
// authoritative for the sender side alone must not consume the local
// recipient's copy.
func TestAuthorizeConsumeRequiresTheCallerToHoldTheRecipientsAuthority(t *testing.T) {
	a := authz(t, "local", "remote")
	senderOnly := notice(agent("remote", "a"), agent("local", "victim"))
	if err := a.authorizeConsume(peerID, senderOnly, senderOnly.To); !errors.Is(err, errForbidden) {
		t.Errorf("a sender-side peer consumed the local recipient's copy: %v", err)
	}
	recipientSide := notice(agent("local", "a"), agent("remote", "mine"))
	if err := a.authorizeConsume(peerID, recipientSide, recipientSide.To); err != nil {
		t.Errorf("a recipient-side peer consuming for its own recipient: %v", err)
	}
	if err := a.authorizeConsume(peerID, recipientSide, recipientSide.From); !errors.Is(err, errForbidden) {
		t.Errorf("a recipient-side peer consumed for the local sender: %v", err)
	}
	both := Identity{Label: "both", Authorities: []string{"remote", "local"}}
	for _, r := range []gomsg.Address{senderOnly.To, senderOnly.From} {
		if err := a.authorizeConsume(both, senderOnly, r); err != nil {
			t.Errorf("a caller holding both sides consuming for %s: %v", r.URN(), err)
		}
	}
}

func TestThreadViewShowsOnlyTheCallersEnvelopes(t *testing.T) {
	a := authz(t, "local")
	mine := notice(agent("remote", "x"), agent("local", "y"))
	theirs := notice(agent("local", "a"), agent("local", "b"))
	other := notice(agent("third", "c"), agent("elsewhere", "d"))
	got := a.threadView(peerID, []gomsg.Envelope{mine, theirs, other})
	if len(got) != 1 || got[0].From != mine.From {
		t.Fatalf("view = %+v, want only the peer's own envelope", got)
	}
	if got := a.threadView(peerID, []gomsg.Envelope{theirs, other}); got == nil || len(got) != 0 {
		t.Fatalf("a thread the caller is not part of must be an empty (non-nil) view, got %#v", got)
	}
}

func TestAuthorizeMailboxNeedsASharedAuthority(t *testing.T) {
	a := authz(t, "local", "shared")
	shared := Identity{Label: "p", Authorities: []string{"shared"}}
	if err := a.authorizeMailbox(OpInbox, shared, agent("shared", "u")); err != nil {
		t.Errorf("a peer draining a mailbox of an authority both installs hold: %v", err)
	}
	if err := a.authorizeMailbox(OpInbox, shared, agent("local", "u")); !errors.Is(err, errForbidden) {
		t.Errorf("a peer must not drain a local-only authority's mailbox: %v", err)
	}
	if err := a.authorizeMailbox(OpSubscribe, peerID, agent("remote", "u")); !errors.Is(err, errNotHomed) {
		t.Errorf("a mailbox of an authority this install does not home: %v", err)
	}
}

func TestGoverningAuthority(t *testing.T) {
	a := authz(t, "local")
	env := notice(agent("remote", "x"), agent("local", "y"))
	if got := a.governingAuthority(OpSend, env); got != "local" {
		t.Errorf("send: %s", got)
	}
	if got := a.governingAuthority(OpGet, notice(agent("local", "x"), agent("remote", "y"))); got != "local" {
		t.Errorf("get: %s", got)
	}
	if got := a.governingAuthority(OpGet, notice(agent("remote", "x"), agent("elsewhere", "y"))); got != "remote" {
		t.Errorf("get with neither homed falls back to the sender: %s", got)
	}
}
