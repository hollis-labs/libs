package federation

import (
	"errors"
	"fmt"

	gomsg "github.com/hollis-labs/go-messaging"
)

// Authorization errors. They are internal: a server maps them to a status and
// never sends their text to the caller.
var (
	// errNotHomed: the governing authority is not one this install homes, so the
	// caller is trying to use this install as a relay. Unroutable: 404.
	errNotHomed = errors.New("federation: governing authority not homed by this install")
	// errForbidden: the caller may not act for the authority in question: 403.
	errForbidden = errors.New("federation: caller not authorized")
)

// authorizer enforces the trust model server-side. It is immutable after
// construction and safe for concurrent use.
//
// Two invariants hold for every call:
//
//   - Routing invariant: this install must home the authority the call is
//     governed by. A request already routed here is checked again, so a peer
//     cannot turn this install into a relay.
//   - Trust boundary: the caller must be a party, authoritative for an authority
//     the target envelope names. A peer may originate mail only for its own
//     authorities and touch only envelopes it is a party to.
type authorizer struct {
	local map[string]struct{}
}

func newAuthorizer(localAuthorities []string) (*authorizer, error) {
	m := make(map[string]struct{}, len(localAuthorities))
	for _, a := range localAuthorities {
		if a == "" {
			return nil, errors.New("federation: local authority entries must be non-empty")
		}
		m[a] = struct{}{}
	}
	if len(m) == 0 {
		return nil, errors.New("federation: at least one local authority is required: an install cannot tell local from foreign without declaring what it homes")
	}
	return &authorizer{local: m}, nil
}

func (a *authorizer) homes(authority string) bool {
	if authority == "" {
		return false
	}
	_, ok := a.local[authority]
	return ok
}

// authorizeSend: the recipient's authority must be homed here, and the sender's
// must be one the caller is authoritative for. Both run before any store call.
func (a *authorizer) authorizeSend(id Identity, env gomsg.Envelope) error {
	if !a.homes(env.To.Authority) {
		return fmt.Errorf("%w: to=%q", errNotHomed, env.To.Authority)
	}
	if !id.IsAuthoritative(env.From.Authority) {
		return fmt.Errorf("%w: %q may not originate mail from=%q (authoritative for %v)",
			errForbidden, id.Label, env.From.Authority, sortedCopy(id.Authorities))
	}
	return nil
}

// authorizeEnvelopeAccess covers Get, Consume and Cancel on one target
// envelope: this install must home a party of it, and the caller must be
// authoritative for a party of it.
func (a *authorizer) authorizeEnvelopeAccess(op Op, id Identity, env gomsg.Envelope) error {
	if !a.homes(env.From.Authority) && !a.homes(env.To.Authority) {
		return fmt.Errorf("%w: %s envelope %q (from=%q to=%q)", errNotHomed, op, env.ID, env.From.Authority, env.To.Authority)
	}
	if !id.IsAuthoritative(env.From.Authority) && !id.IsAuthoritative(env.To.Authority) {
		return fmt.Errorf("%w: %q is not a party to %s envelope %q", errForbidden, id.Label, op, env.ID)
	}
	return nil
}

// authorizeConsume is envelope access plus two checks on the recipient being
// consumed for: it must be one of the envelope's addresses (a party cannot mark
// an envelope consumed for an unrelated recipient), and the caller must be
// authoritative for its authority (a peer holding only the sender side cannot
// consume the local recipient's copy). Both refuse alike.
func (a *authorizer) authorizeConsume(id Identity, env gomsg.Envelope, recipient gomsg.Address) error {
	if err := a.authorizeEnvelopeAccess(OpConsume, id, env); err != nil {
		return err
	}
	if recipient != env.To && recipient != env.From {
		return fmt.Errorf("%w: recipient %s is not an address of envelope %q", errForbidden, recipient.URN(), env.ID)
	}
	if !id.IsAuthoritative(recipient.Authority) {
		return fmt.Errorf("%w: %q is not authoritative for recipient %s of envelope %q", errForbidden, id.Label, recipient.URN(), env.ID)
	}
	return nil
}

// threadView filters a thread to the envelopes the caller is a party to (and this
// install homes a party of). A thread can hold messages between others; a peer
// sees only its own. A thread the caller is not part of yields nothing, not an
// error, so the answer does not tell a peer whether a thread id exists.
func (a *authorizer) threadView(id Identity, envs []gomsg.Envelope) []gomsg.Envelope {
	out := make([]gomsg.Envelope, 0, len(envs))
	for _, e := range envs {
		if a.authorizeEnvelopeAccess(OpThread, id, e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// authorizeMailbox covers Inbox and Subscribe, which are off unless an adopter
// widens the OpSet. A mailbox may be drained only by a caller authoritative for
// the recipient's authority, and only if this install homes that authority too:
// the authority has to be shared between the two installs for a peer to have any
// business with a mailbox held here.
func (a *authorizer) authorizeMailbox(op Op, id Identity, to gomsg.Address) error {
	if !a.homes(to.Authority) {
		return fmt.Errorf("%w: %s to=%q", errNotHomed, op, to.Authority)
	}
	if !id.IsAuthoritative(to.Authority) {
		return fmt.Errorf("%w: %q is not authoritative for the mailbox of %q", errForbidden, id.Label, to.Authority)
	}
	return nil
}

// governingAuthority is the authority a call is logged against.
func (a *authorizer) governingAuthority(op Op, env gomsg.Envelope) string {
	if op == OpSend {
		return env.To.Authority
	}
	if a.homes(env.To.Authority) {
		return env.To.Authority
	}
	return env.From.Authority
}
