package host

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"slices"
	"sync"
	"time"

	cap "github.com/hollis-labs/plugin-sdk/capability"
)

// SubjectKind excludes plugins: plugins use their authenticated stdio binding.
type SubjectKind string

const (
	AgentClient    SubjectKind = "agent"
	SessionClient  SubjectKind = "session"
	MCPProxyClient SubjectKind = "mcp_proxy"
)

type Subject struct {
	Kind SubjectKind
	ID   string
}

func (s Subject) valid() bool {
	return s.ID != "" && (s.Kind == AgentClient || s.Kind == SessionClient || s.Kind == MCPProxyClient)
}

// CredentialClaims are host-authenticated metadata, not a signed wire token.
// Scopes are keyed by grant ID; each ID must have an explicit scope. Issuers
// must first resolve scopes under reviewed grants and current caller policy.
type CredentialClaims struct {
	Subject         Subject
	Owner           cap.RuntimeIdentity
	Audience        string
	GrantIDs        []string
	Scopes          map[string]cap.Scope
	CapabilityNames map[string]string
}

// Timer and Clock permit deterministic expiry tests without changing wall time.
// AfterFunc must schedule asynchronously, never invoke inline, and must not
// invoke the callback before the requested duration. Stop must not wait for it.
type Timer interface{ Stop() bool }
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}
type wallClock struct{}

func (wallClock) Now() time.Time                            { return time.Now() }
func (wallClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

const DefaultCredentialLease = 5 * time.Minute

// CredentialConfig pins one process epoch and audience. MaxLease is a ceiling;
// zero chooses five minutes. Clock defaults to the system clock.
type CredentialConfig struct {
	HostInstance, Audience string
	MaxLease               time.Duration
	Clock                  Clock
}

type credentialEntry struct {
	hash    [32]byte
	claims  CredentialClaims
	expires time.Time
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timer   Timer
}

// CredentialStore retains only token hashes and authority metadata. It must be
// owned by one live host process, closed on shutdown and never serialized.
type CredentialStore struct {
	mu             sync.Mutex
	host, audience string
	maxLease       time.Duration
	clock          Clock
	entries        map[[32]byte]*credentialEntry
	generations    map[string]uint64
	active         map[string]uint64
	closed         bool
}

func NewCredentialStore(cfg CredentialConfig) (*CredentialStore, error) {
	if cfg.HostInstance == "" || cfg.Audience == "" || cfg.MaxLease < 0 {
		return nil, refusal(cap.InvalidRequest, "")
	}
	if cfg.MaxLease == 0 {
		cfg.MaxLease = DefaultCredentialLease
	}
	if cfg.Clock == nil {
		cfg.Clock = wallClock{}
	}
	return &CredentialStore{host: cfg.HostInstance, audience: cfg.Audience, maxLease: cfg.MaxLease, clock: cfg.Clock, entries: map[[32]byte]*credentialEntry{}, generations: map[string]uint64{}, active: map[string]uint64{}}, nil
}

// ActivateOwner installs a host-issued incarnation and cancels all earlier
// leases for that owner. Repeating the current activation is idempotent; a
// fenced generation cannot be resurrected or a counter reset within an epoch.
func (s *CredentialStore) ActivateOwner(tuple cap.RuntimeIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || tuple.Validate() != nil || tuple.HostInstance != s.host {
		return refusal(cap.Unauthenticated, "")
	}
	if s.active[tuple.OwnerID] == tuple.OwnerGeneration {
		return nil
	}
	if tuple.OwnerGeneration <= s.generations[tuple.OwnerID] {
		return refusal(cap.Unauthenticated, "")
	}
	s.revokeOwnerLocked(tuple.OwnerID)
	s.generations[tuple.OwnerID] = tuple.OwnerGeneration
	s.active[tuple.OwnerID] = tuple.OwnerGeneration
	return nil
}

// IssuedCredential exposes the raw token once, to a private control channel.
// Formatting redacts it; adapters must still exclude it from JSON/log payloads.
type IssuedCredential struct {
	Token     string
	ExpiresAt time.Time
}

func (IssuedCredential) String() string   { return "[credential redacted]" }
func (IssuedCredential) GoString() string { return "[credential redacted]" }
func (IssuedCredential) Format(state fmt.State, verb rune) {
	fmt.Fprint(state, "[credential redacted]")
}

// Issue accepts claims from a trusted issuer after host authentication/approval.
// The library cannot authenticate a control channel or infer reviewed policy.
func (s *CredentialStore) Issue(claims CredentialClaims, lease time.Duration) (IssuedCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkClaimsLocked(claims); err != nil {
		return IssuedCredential{}, err
	}
	return s.issueLocked(claims, lease)
}
func (s *CredentialStore) checkClaimsLocked(c CredentialClaims) error {
	if s.closed || !c.Subject.valid() || c.Owner.Validate() != nil || c.Owner.HostInstance != s.host || c.Audience != s.audience || s.active[c.Owner.OwnerID] != c.Owner.OwnerGeneration {
		return refusal(cap.Unauthenticated, "")
	}
	if len(c.GrantIDs) == 0 || !validNames(c.GrantIDs) || len(c.Scopes) != len(c.GrantIDs) || len(c.CapabilityNames) != len(c.GrantIDs) {
		return refusal(cap.InvalidRequest, "")
	}
	for _, id := range c.GrantIDs {
		scope, ok := c.Scopes[id]
		if !ok || c.CapabilityNames[id] == "" {
			return refusal(cap.InvalidRequest, "")
		}
		if err := scope.Validate(c.CapabilityNames[id]); err != nil {
			return err
		}
	}
	return nil
}
func (s *CredentialStore) issueLocked(claims CredentialClaims, lease time.Duration) (IssuedCredential, error) {
	if lease == 0 {
		lease = min(DefaultCredentialLease, s.maxLease)
	}
	if lease <= 0 || lease > s.maxLease {
		return IssuedCredential{}, refusal(cap.InvalidRequest, "")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return IssuedCredential{}, refusal(cap.InternalError, "")
	}
	hash := sha256.Sum256(secret[:])
	if _, exists := s.entries[hash]; exists {
		return IssuedCredential{}, refusal(cap.InternalError, "")
	}
	expiry := s.clock.Now().Add(lease)
	ctx, cancel := context.WithCancelCause(context.Background())
	entry := &credentialEntry{hash: hash, claims: cloneClaims(claims), expires: expiry, ctx: ctx, cancel: cancel}
	s.entries[hash] = entry
	entry.timer = s.clock.AfterFunc(lease, func() { s.expire(hash, entry) })
	return IssuedCredential{base64.RawURLEncoding.EncodeToString(secret[:]), expiry}, nil
}
func (s *CredentialStore) expire(hash [32]byte, entry *credentialEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[hash] == entry {
		// A clock implementation must schedule callbacks no earlier than requested.
		s.removeLocked(hash, refusal(cap.Unauthenticated, ""))
	}
}
func tokenHash(token string) ([32]byte, bool) {
	if len(token) != 43 {
		return [32]byte{}, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}
func (s *CredentialStore) findLocked(token string) ([32]byte, *credentialEntry, error) {
	hash, ok := tokenHash(token)
	entry := s.entries[hash]
	if !ok || entry == nil || subtle.ConstantTimeCompare(hash[:], entry.hash[:]) != 1 {
		return hash, nil, refusal(cap.Unauthenticated, "")
	}
	if s.closed || !s.clock.Now().Before(entry.expires) || s.active[entry.claims.Owner.OwnerID] != entry.claims.Owner.OwnerGeneration {
		s.removeLocked(hash, refusal(cap.Unauthenticated, ""))
		return hash, nil, refusal(cap.Unauthenticated, "")
	}
	return hash, entry, nil
}

// CredentialLease is a copied authority snapshot with a cancellation context.
// Callers must reverify at dispatch and after waiting, before mutation. Holding
// this snapshot alone never protects against a later revocation race.
type CredentialLease struct {
	Claims    CredentialClaims
	ExpiresAt time.Time
	Context   context.Context
}

func (s *CredentialStore) Verify(token string, subject Subject, owner cap.RuntimeIdentity, audience string) (CredentialLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, entry, err := s.findLocked(token)
	if err != nil {
		return CredentialLease{}, err
	}
	if entry.claims.Subject != subject || entry.claims.Owner != owner || entry.claims.Audience != audience {
		return CredentialLease{}, refusal(cap.Unauthenticated, "")
	}
	return CredentialLease{cloneClaims(entry.claims), entry.expires, entry.ctx}, nil
}

// Renew rotates the credential on an authenticated live owner/control channel.
// The adapter must verify that channel before invoking this method. Subject,
// tuple and audience are immutable; grants/scopes may only narrow. Old leases
// are cancelled immediately after the replacement is issued successfully.
func (s *CredentialStore) Renew(token string, next CredentialClaims, lease time.Duration) (IssuedCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, old, err := s.findLocked(token)
	if err != nil {
		return IssuedCredential{}, err
	}
	if err := s.checkClaimsLocked(next); err != nil {
		return IssuedCredential{}, err
	}
	if old.claims.Subject != next.Subject || old.claims.Owner != next.Owner || old.claims.Audience != next.Audience {
		return IssuedCredential{}, refusal(cap.Unauthenticated, "")
	}
	for _, id := range next.GrantIDs {
		if !slices.Contains(old.claims.GrantIDs, id) {
			return IssuedCredential{}, refusal(cap.ScopeDenied, next.CapabilityNames[id])
		}
		if next.CapabilityNames[id] != old.claims.CapabilityNames[id] {
			return IssuedCredential{}, refusal(cap.ScopeDenied, next.CapabilityNames[id])
		}
		if err := cap.CheckNarrowing(old.claims.CapabilityNames[id], old.claims.Scopes[id], next.Scopes[id]); err != nil {
			return IssuedCredential{}, err
		}
	}
	replacement, err := s.issueLocked(next, lease)
	if err != nil {
		return IssuedCredential{}, err
	}
	s.removeLocked(hash, refusal(cap.Unauthenticated, ""))
	return replacement, nil
}
func (s *CredentialStore) removeLocked(hash [32]byte, cause error) {
	if entry := s.entries[hash]; entry != nil {
		delete(s.entries, hash)
		if entry.timer != nil {
			entry.timer.Stop()
		}
		entry.cancel(cause)
	}
}
func (s *CredentialStore) revokeOwnerLocked(ownerID string) {
	delete(s.active, ownerID)
	for hash, entry := range s.entries {
		if entry.claims.Owner.OwnerID == ownerID {
			s.removeLocked(hash, refusal(cap.Unauthenticated, ""))
		}
	}
}

// RevokeOwner fences exactly one incarnation; stale cleanup cannot revoke its
// replacement. Call on stop/disable/unload/disconnect/policy withdrawal.
func (s *CredentialStore) RevokeOwner(tuple cap.RuntimeIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tuple.HostInstance == s.host && s.active[tuple.OwnerID] == tuple.OwnerGeneration {
		s.revokeOwnerLocked(tuple.OwnerID)
	}
}
func (s *CredentialStore) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hash, ok := tokenHash(token); ok {
		s.removeLocked(hash, refusal(cap.Unauthenticated, ""))
	}
}
func (s *CredentialStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for hash := range s.entries {
		s.removeLocked(hash, refusal(cap.Unauthenticated, ""))
	}
	clear(s.active)
}
func cloneScope(s cap.Scope) cap.Scope {
	out := cap.Scope{Allowlists: map[string][]string{}, Limits: map[string]int64{}}
	for k, v := range s.Allowlists {
		out.Allowlists[k] = slices.Clone(v)
	}
	for k, v := range s.Limits {
		out.Limits[k] = v
	}
	return out
}
func cloneClaims(c CredentialClaims) CredentialClaims {
	c.GrantIDs = slices.Clone(c.GrantIDs)
	scopes := map[string]cap.Scope{}
	for k, v := range c.Scopes {
		scopes[k] = cloneScope(v)
	}
	c.Scopes = scopes
	names := map[string]string{}
	for id, name := range c.CapabilityNames {
		names[id] = name
	}
	c.CapabilityNames = names
	return c
}
