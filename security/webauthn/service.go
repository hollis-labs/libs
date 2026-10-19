package webauthn

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	wa "github.com/go-webauthn/webauthn/webauthn"
)

// Service adds durable single-use ceremonies and atomic counter updates to
// Party. The caller authenticates the subject and authorizes every operation,
// including enrollment/first-key bootstrap, before calling these methods.
// It is not an identity provider, HTTP handler or operation-proof policy engine.
type Service struct {
	party *Party
	store Store
	now   func() time.Time
}

func NewService(party *Party, store Store, now func() time.Time) (*Service, error) {
	if party == nil || store == nil {
		return nil, errors.New("webauthn: party and store required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{party: party, store: store, now: now}, nil
}

// Challenge hashes length-prefixed fields. This preserves the Cerberus v2
// challenge format; callers using a different profile supply its exact bytes
// to BeginAssertion instead. It is not a canonical JSON implementation.
func Challenge(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(p))
		_, _ = h.Write(p)
	}
	return h.Sum(nil)
}
func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Service) load(ctx context.Context, subject string) (Snapshot, error) {
	st, err := s.store.Load(ctx, subject)
	if err != nil {
		return st, err
	}
	if len(st.User.ID) == 0 {
		return st, ErrUnknownCredential
	}
	if st.Pending == nil {
		st.Pending = map[string]Ceremony{}
	}
	if st.Proofs == nil {
		st.Proofs = map[string]Proof{}
	}
	return st, nil
}
func (s *Service) prune(st *Snapshot) {
	now := s.now()
	for id, c := range st.Pending {
		if !now.Before(c.ExpiresAt) {
			delete(st.Pending, id)
		}
	}
	for id, p := range st.Proofs {
		if !now.Before(p.ExpiresAt) {
			delete(st.Proofs, id)
		}
	}
}
func (s *Service) expiry(deadline time.Time) (time.Time, error) {
	now := s.now()
	if !deadline.After(now) {
		return time.Time{}, ErrCeremony
	}
	if max := now.Add(5 * time.Minute); deadline.After(max) {
		deadline = max
	}
	return deadline, nil
}

// BeginAssertion persists the exact challenge and origin before returning
// browser options. The challenge must include an unpredictable nonce and bind
// the operation; the generated ceremony ID is NOT automatically in that hash.
func (s *Service) BeginAssertion(ctx context.Context, subject string, challenge []byte, origin string, deadline time.Time) (string, json.RawMessage, error) {
	st, err := s.load(ctx, subject)
	if err != nil {
		return "", nil, err
	}
	expires, err := s.expiry(deadline)
	if err != nil {
		return "", nil, err
	}
	opts, err := s.party.BeginAssertion(st.User, challenge, origin)
	if err != nil {
		return "", nil, err
	}
	id := randomID()
	s.prune(&st)
	c := Ceremony{Kind: "assert", Challenge: append([]byte(nil), challenge...), Origin: origin, ExpiresAt: expires}
	for _, cred := range st.User.Credentials {
		c.AllowedIDs = append(c.AllowedIDs, cred.ID)
	}
	st.Pending[id] = c
	if err = s.store.CompareAndSwap(ctx, subject, st.Version, st); err != nil {
		return "", nil, err
	}
	return id, opts, nil
}
func ceremonyUser(u User, c Ceremony) User {
	out := u
	out.Credentials = nil
	for _, cred := range u.Credentials {
		for _, id := range c.AllowedIDs {
			if bytes.Equal(cred.ID, id) {
				out.Credentials = append(out.Credentials, cred)
				break
			}
		}
	}
	return out
}
func updateCredential(u *User, c *wa.Credential) {
	for i := range u.Credentials {
		if bytes.Equal(u.Credentials[i].ID, c.ID) {
			u.Credentials[i] = *c
			return
		}
	}
}

// FinishAssertion atomically spends the ceremony, advances the counter and
// records the original assertion. Storage failure or a concurrent mutation
// refuses the result. A failed signature consumes the ceremony as well.
func (s *Service) FinishAssertion(ctx context.Context, subject, id string, raw json.RawMessage) (Proof, error) {
	st, err := s.load(ctx, subject)
	if err != nil {
		return Proof{}, err
	}
	c, ok := st.Pending[id]
	if !ok || c.Kind != "assert" || !s.now().Before(c.ExpiresAt) {
		return Proof{}, ErrCeremony
	}
	delete(st.Pending, id)
	cred, verifyErr := s.party.VerifyAssertion(ceremonyUser(st.User, c), raw, c.Challenge, c.Origin)
	if verifyErr == nil && !s.now().Before(c.ExpiresAt) {
		verifyErr = ErrCeremony
	}
	if verifyErr == nil && cred.Authenticator.CloneWarning {
		verifyErr = ErrCounter
	}
	var out Proof
	if verifyErr == nil {
		updateCredential(&st.User, cred)
		out = Proof{CredentialID: cred.ID, Challenge: c.Challenge, Origin: c.Origin, Assertion: append(json.RawMessage(nil), raw...), VerifiedAt: s.now().UTC(), ExpiresAt: c.ExpiresAt}
		st.Proofs[id] = out
	}
	if err = s.store.CompareAndSwap(ctx, subject, st.Version, st); err != nil {
		return Proof{}, err
	}
	return out, verifyErr
}

// Reverify checks only a proof already recorded by FinishAssertion, against
// current credentials and the caller's current expected challenge. It does not
// advance the counter again or consume the application's continuation.
func (s *Service) Reverify(ctx context.Context, subject, id string, challenge []byte) (Proof, error) {
	st, err := s.load(ctx, subject)
	if err != nil {
		return Proof{}, err
	}
	p, ok := st.Proofs[id]
	if !ok || !s.now().Before(p.ExpiresAt) || !bytes.Equal(p.Challenge, challenge) {
		return Proof{}, ErrCeremony
	}
	u := st.User
	u.Credentials = append([]wa.Credential(nil), u.Credentials...)
	for i := range u.Credentials {
		u.Credentials[i].Authenticator.SignCount = 0
		u.Credentials[i].Authenticator.CloneWarning = false
	}
	cred, err := s.party.VerifyAssertion(u, p.Assertion, challenge, p.Origin)
	if err != nil {
		return Proof{}, err
	}
	if !s.now().Before(p.ExpiresAt) {
		return Proof{}, ErrCeremony
	}
	if !bytes.Equal(cred.ID, p.CredentialID) {
		return Proof{}, ErrAssertion
	}
	return p, nil
}

// BeginRegistration requires caller authorization for enrollment. Existing
// credential authorization or first-key bootstrap policy is application-owned.
func (s *Service) BeginRegistration(ctx context.Context, subject, origin string, deadline time.Time) (string, json.RawMessage, error) {
	st, err := s.load(ctx, subject)
	if err != nil {
		return "", nil, err
	}
	expires, err := s.expiry(deadline)
	if err != nil {
		return "", nil, err
	}
	raw, session, err := s.party.BeginRegistration(st.User, origin)
	if err != nil {
		return "", nil, err
	}
	id := randomID()
	s.prune(&st)
	st.Pending[id] = Ceremony{Kind: "register", Origin: origin, ExpiresAt: expires, Registration: session}
	if err = s.store.CompareAndSwap(ctx, subject, st.Version, st); err != nil {
		return "", nil, err
	}
	return id, raw, nil
}

// FinishRegistration validates and stores a new public credential. The caller
// must recheck enrollment authorization before calling this method.
func (s *Service) FinishRegistration(ctx context.Context, subject, id string, raw json.RawMessage) (*wa.Credential, error) {
	st, err := s.load(ctx, subject)
	if err != nil {
		return nil, err
	}
	c, ok := st.Pending[id]
	if !ok || c.Kind != "register" || c.Registration == nil || !s.now().Before(c.ExpiresAt) {
		return nil, ErrCeremony
	}
	delete(st.Pending, id)
	cred, verifyErr := s.party.FinishRegistration(st.User, *c.Registration, raw)
	if verifyErr == nil && !s.now().Before(c.ExpiresAt) {
		verifyErr = ErrCeremony
	}
	if verifyErr == nil {
		for _, old := range st.User.Credentials {
			if bytes.Equal(old.ID, cred.ID) {
				verifyErr = ErrConflict
			}
		}
	}
	if verifyErr == nil {
		st.User.Credentials = append(st.User.Credentials, *cred)
	}
	if err = s.store.CompareAndSwap(ctx, subject, st.Version, st); err != nil {
		return nil, err
	}
	if verifyErr != nil {
		return nil, verifyErr
	}
	return cred, nil
}

// BeginRemoval binds an enrolled-key assertion to this subject and exact
// credential ID. The caller's removal policy remains independently required.
func (s *Service) BeginRemoval(ctx context.Context, subject string, target []byte, origin string, deadline time.Time) (string, json.RawMessage, error) {
	st, err := s.load(ctx, subject)
	if err != nil {
		return "", nil, err
	}
	found := false
	for _, c := range st.User.Credentials {
		if bytes.Equal(c.ID, target) {
			found = true
		}
	}
	if !found {
		return "", nil, ErrUnknownCredential
	}
	expires, err := s.expiry(deadline)
	if err != nil {
		return "", nil, err
	}
	id := randomID()
	challenge := Challenge([]byte("webauthn remove v1"), []byte(subject), target, []byte(id), []byte(expires.UTC().Format(time.RFC3339Nano)))
	opts, err := s.party.BeginAssertion(st.User, challenge, origin)
	if err != nil {
		return "", nil, err
	}
	s.prune(&st)
	c := Ceremony{Kind: "remove", Challenge: challenge, Origin: origin, ExpiresAt: expires, RemoveID: append([]byte(nil), target...)}
	for _, cred := range st.User.Credentials {
		c.AllowedIDs = append(c.AllowedIDs, cred.ID)
	}
	st.Pending[id] = c
	if err = s.store.CompareAndSwap(ctx, subject, st.Version, st); err != nil {
		return "", nil, err
	}
	return id, opts, nil
}
func (s *Service) FinishRemoval(ctx context.Context, subject, id string, raw json.RawMessage) error {
	st, err := s.load(ctx, subject)
	if err != nil {
		return err
	}
	c, ok := st.Pending[id]
	if !ok || c.Kind != "remove" || !s.now().Before(c.ExpiresAt) {
		return ErrCeremony
	}
	delete(st.Pending, id)
	cred, verifyErr := s.party.VerifyAssertion(ceremonyUser(st.User, c), raw, c.Challenge, c.Origin)
	if verifyErr == nil && !s.now().Before(c.ExpiresAt) {
		verifyErr = ErrCeremony
	}
	if verifyErr == nil && cred.Authenticator.CloneWarning {
		verifyErr = ErrCounter
	}
	if verifyErr == nil {
		updateCredential(&st.User, cred)
		kept := st.User.Credentials[:0]
		for _, key := range st.User.Credentials {
			if !bytes.Equal(key.ID, c.RemoveID) {
				kept = append(kept, key)
			}
		}
		st.User.Credentials = kept
		// Removing a key also invalidates its receipts and eligibility in existing
		// ceremonies, even if that credential ID is subsequently enrolled again.
		for proofID, receipt := range st.Proofs {
			if bytes.Equal(receipt.CredentialID, c.RemoveID) {
				delete(st.Proofs, proofID)
			}
		}
		for pendingID, pending := range st.Pending {
			ids := pending.AllowedIDs[:0]
			for _, allowedID := range pending.AllowedIDs {
				if !bytes.Equal(allowedID, c.RemoveID) {
					ids = append(ids, allowedID)
				}
			}
			pending.AllowedIDs = ids
			st.Pending[pendingID] = pending
		}
	}
	if err = s.store.CompareAndSwap(ctx, subject, st.Version, st); err != nil {
		return err
	}
	return verifyErr
}
