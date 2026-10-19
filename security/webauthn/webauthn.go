package webauthn

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/go-webauthn/webauthn/protocol"
	wa "github.com/go-webauthn/webauthn/webauthn"
)

var (
	ErrAssertion         = errors.New("webauthn: invalid assertion")
	ErrUnknownCredential = errors.New("webauthn: unknown credential")
	ErrCounter           = errors.New("webauthn: signature counter did not advance")
	ErrConflict          = errors.New("webauthn: credential state changed")
	ErrCeremony          = errors.New("webauthn: unknown, used or expired ceremony")
	ErrBinding           = errors.New("webauthn: ceremony caller binding mismatch")
	ErrChallengeReuse    = errors.New("webauthn: assertion challenge already used")
)

// User comes from the caller's trusted identity store, never a browser's claims.
// ID is the opaque WebAuthn user handle. Credentials contain only public keys.
type User struct {
	ID          []byte
	Name        string
	DisplayName string
	Credentials []wa.Credential
}

func (u User) WebAuthnID() []byte                   { return u.ID }
func (u User) WebAuthnName() string                 { return u.Name }
func (u User) WebAuthnDisplayName() string          { return u.DisplayName }
func (u User) WebAuthnCredentials() []wa.Credential { return u.Credentials }

// Party pins a relying party and its trusted origin allow-list. Construct it
// from server configuration, not assertion fields. Authorization, operation
// canonicalization, ceremony consumption and counter persistence belong to the
// caller or Service, not these stateless primitives.
type Party struct{ rp *wa.WebAuthn }

func NewParty(rpID, displayName string, origins []string) (*Party, error) {
	rp, err := wa.New(&wa.Config{RPID: rpID, RPDisplayName: displayName, RPOrigins: append([]string(nil), origins...)})
	if err != nil {
		return nil, err
	}
	return &Party{rp: rp}, nil
}
func descriptors(u User) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(u.Credentials))
	for _, c := range u.Credentials {
		out = append(out, c.Descriptor())
	}
	return out
}

// BeginAssertion constructs UV-required options for caller-supplied challenge
// bytes. Bind all operation fields, nonce and expiry before calling this.
func (p *Party) BeginAssertion(u User, challenge []byte, origin string) (json.RawMessage, error) {
	if len(challenge) < 16 {
		return nil, ErrAssertion
	}
	opts, _, err := p.rp.BeginLogin(u, wa.WithChallenge(challenge), wa.WithLoginOrigin(origin),
		wa.WithUserVerification(protocol.VerificationRequired), wa.WithAllowedCredentials(descriptors(u)))
	if err != nil {
		return nil, err
	}
	return json.Marshal(opts)
}

// VerifyAssertion checks the original assertion bytes against trusted user,
// challenge and origin. It returns the upstream counter result: callers MUST
// reject CloneWarning for a new decision and persist SignCount atomically.
// Rechecking an already recorded assertion is not a new counter event.
func (p *Party) VerifyAssertion(u User, raw, challenge []byte, origin string) (*wa.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		return nil, ErrAssertion
	}
	ids := make([][]byte, 0, len(u.Credentials))
	found := false
	for _, c := range u.Credentials {
		ids = append(ids, c.ID)
		if bytes.Equal(c.ID, parsed.RawID) {
			found = true
		}
	}
	if !found {
		return nil, ErrUnknownCredential
	}
	session := wa.SessionData{Challenge: base64.RawURLEncoding.EncodeToString(challenge),
		RelyingPartyID: p.rp.Config.RPID, Origin: origin, UserID: u.ID, AllowedCredentialIDs: ids, UserVerification: protocol.VerificationRequired}
	credential, err := p.rp.ValidateLogin(u, session, parsed)
	if err != nil {
		return nil, ErrAssertion
	}
	return credential, nil
}

// BeginRegistration creates UV-required registration options. The caller must
// authorize enrollment (including first-key bootstrap) before exposing them.
func (p *Party) BeginRegistration(u User, origin string) (json.RawMessage, *wa.SessionData, error) {
	opts, session, err := p.rp.BeginRegistration(u,
		wa.WithAuthenticatorSelection(protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired, ResidentKey: protocol.ResidentKeyRequirementPreferred}),
		wa.WithConveyancePreference(protocol.PreferNoAttestation), wa.WithExclusions(descriptors(u)), wa.WithRegistrationOrigin(origin))
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(opts)
	return raw, session, err
}

// FinishRegistration validates against the original trusted session. It does
// not authorize or persist enrollment.
func (p *Party) FinishRegistration(u User, session wa.SessionData, raw []byte) (*wa.Credential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBytes(raw)
	if err != nil {
		return nil, ErrAssertion
	}
	credential, err := p.rp.CreateCredential(u, session, parsed)
	if err != nil {
		return nil, ErrAssertion
	}
	if !credential.Flags.UserVerified {
		return nil, ErrAssertion
	}
	return credential, nil
}
