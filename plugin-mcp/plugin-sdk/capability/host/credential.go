package host

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
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
	Owner           capability.RuntimeIdentity
	Audience        string
	GrantIDs        []string
	Scopes          map[string]capability.Scope
	CapabilityNames map[string]string
	GrantExpiresAt  map[string]time.Time
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

func (wallClock) Now() time.Time                            { return time.Now().Round(0) }
func (wallClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

const DefaultCredentialLease = 5 * time.Minute
const DefaultCredentialLifetime = time.Hour

// CredentialConfig pins one process epoch and audience. MaxLease is a ceiling;
// zero chooses five minutes. MaxLifetime bounds the entire renewal chain and
// defaults to one hour. Clock defaults to wall time without monotonic readings.
type CredentialConfig struct {
	HostInstance, Audience string
	MaxLease               time.Duration
	MaxLifetime            time.Duration
	Clock                  Clock
}

type credentialEntry struct {
	hash        [32]byte
	leaseID     string
	lifetimeEnd time.Time
	claims      CredentialClaims
	expires     time.Time
	ctx         context.Context
	cancel      context.CancelCauseFunc
	timer       Timer
}

// CredentialStore retains only token hashes and authority metadata. It must be
// owned by one live host process, closed on shutdown and never serialized.
type CredentialStore struct {
	mu             sync.Mutex
	host, audience string
	maxLease       time.Duration
	maxLifetime    time.Duration
	leases         map[string][32]byte
	clock          Clock
	entries        map[[32]byte]*credentialEntry
	generations    map[string]uint64
	active         map[string]uint64
	closed         bool
}

func NewCredentialStore(cfg CredentialConfig) (*CredentialStore, error) {
	if cfg.HostInstance == "" || cfg.Audience == "" || cfg.MaxLease < 0 || cfg.MaxLifetime < 0 {
		return nil, refusal(capability.InvalidRequest, "")
	}
	if cfg.MaxLease == 0 {
		cfg.MaxLease = DefaultCredentialLease
	}
	if cfg.MaxLifetime == 0 {
		cfg.MaxLifetime = DefaultCredentialLifetime
	}
	if cfg.Clock == nil {
		cfg.Clock = wallClock{}
	}
	return &CredentialStore{host: cfg.HostInstance, audience: cfg.Audience, maxLease: cfg.MaxLease, maxLifetime: cfg.MaxLifetime, leases: map[string][32]byte{}, clock: cfg.Clock, entries: map[[32]byte]*credentialEntry{}, generations: map[string]uint64{}, active: map[string]uint64{}}, nil
}

// ActivateOwner installs a host-issued incarnation and cancels all earlier
// leases for that owner. Repeating the current activation is idempotent; a
// fenced generation cannot be resurrected or a counter reset within an epoch.
func (s *CredentialStore) ActivateOwner(tuple capability.RuntimeIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || tuple.Validate() != nil || tuple.HostInstance != s.host {
		return refusal(capability.Unauthenticated, "")
	}
	if s.active[tuple.OwnerID] == tuple.OwnerGeneration {
		return nil
	}
	if tuple.OwnerGeneration <= s.generations[tuple.OwnerID] {
		return refusal(capability.Unauthenticated, "")
	}
	s.revokeOwnerLocked(tuple.OwnerID)
	s.generations[tuple.OwnerID] = tuple.OwnerGeneration
	s.active[tuple.OwnerID] = tuple.OwnerGeneration
	return nil
}

// IssuedCredential carries a non-secret lease handle and an opaque one-shot
// secret. Even copies share the same Reveal state. Secret-bearing JSON/text,
// slog and nested formatting paths are redacted; Reveal is the only access.
type IssuedCredential struct {
	secret    *credentialSecret
	LeaseID   string
	ExpiresAt time.Time
}
type credentialSecret struct {
	mu    sync.Mutex
	token string
}

func (c IssuedCredential) Reveal() (string, error) {
	if c.secret == nil {
		return "", refusal(capability.InvalidRequest, "")
	}
	c.secret.mu.Lock()
	defer c.secret.mu.Unlock()
	if c.secret.token == "" {
		return "", refusal(capability.InvalidRequest, "")
	}
	token := c.secret.token
	c.secret.token = ""
	return token, nil
}
func (IssuedCredential) String() string   { return "[credential redacted]" }
func (IssuedCredential) GoString() string { return "[credential redacted]" }
func (IssuedCredential) Format(state fmt.State, verb rune) {
	fmt.Fprint(state, "[credential redacted]")
}
func (IssuedCredential) MarshalJSON() ([]byte, error) { return []byte(`"[credential redacted]"`), nil }
func (IssuedCredential) MarshalText() ([]byte, error) { return []byte("[credential redacted]"), nil }
func (IssuedCredential) LogValue() slog.Value         { return slog.StringValue("[credential redacted]") }

// Issue accepts claims from a trusted issuer after host authentication/approval.
// The library cannot authenticate a control channel or infer reviewed policy.
func (s *CredentialStore) Issue(claims CredentialClaims, lease time.Duration) (IssuedCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkClaimsLocked(claims); err != nil {
		return IssuedCredential{}, err
	}
	return s.issueLocked(claims, lease, "", time.Time{})
}
func (s *CredentialStore) checkClaimsLocked(c CredentialClaims) error {
	if s.closed || !c.Subject.valid() || c.Owner.Validate() != nil || c.Owner.HostInstance != s.host || c.Audience != s.audience || s.active[c.Owner.OwnerID] != c.Owner.OwnerGeneration {
		return refusal(capability.Unauthenticated, "")
	}
	if len(c.GrantIDs) == 0 || !validNames(c.GrantIDs) || len(c.Scopes) != len(c.GrantIDs) || len(c.CapabilityNames) != len(c.GrantIDs) || len(c.GrantExpiresAt) != len(c.GrantIDs) {
		return refusal(capability.InvalidRequest, "")
	}
	for _, id := range c.GrantIDs {
		scope, ok := c.Scopes[id]
		if !ok || !capabilityName(c.CapabilityNames[id]) {
			return refusal(capability.InvalidRequest, "")
		}
		if !c.GrantExpiresAt[id].Round(0).After(s.clock.Now().Round(0)) {
			return refusal(capability.CapabilityDenied, c.CapabilityNames[id])
		}
		if err := scope.Validate(c.CapabilityNames[id]); err != nil {
			return err
		}
	}
	return nil
}
func (s *CredentialStore) issueLocked(claims CredentialClaims, lease time.Duration, leaseID string, lifetimeEnd time.Time) (IssuedCredential, error) {
	if lease == 0 {
		lease = min(DefaultCredentialLease, s.maxLease)
	}
	if lease <= 0 || lease > s.maxLease {
		return IssuedCredential{}, refusal(capability.InvalidRequest, "")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return IssuedCredential{}, refusal(capability.InternalError, "")
	}
	hash := sha256.Sum256(secret[:])
	if _, exists := s.entries[hash]; exists {
		return IssuedCredential{}, refusal(capability.InternalError, "")
	}
	now := s.clock.Now().Round(0)
	if lifetimeEnd.IsZero() {
		lifetimeEnd = now.Add(s.maxLifetime)
	}
	expiry := minTime(now.Add(lease), lifetimeEnd)
	for _, grantExpiry := range claims.GrantExpiresAt {
		expiry = minTime(expiry, grantExpiry.Round(0))
	}
	if !expiry.After(now) {
		return IssuedCredential{}, refusal(capability.Unauthenticated, "")
	}
	if leaseID == "" {
		var handle [16]byte
		if _, err := rand.Read(handle[:]); err != nil {
			return IssuedCredential{}, refusal(capability.InternalError, "")
		}
		leaseID = base64.RawURLEncoding.EncodeToString(handle[:])
		if _, exists := s.leases[leaseID]; exists {
			return IssuedCredential{}, refusal(capability.InternalError, "")
		}
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	entry := &credentialEntry{hash: hash, leaseID: leaseID, lifetimeEnd: lifetimeEnd, claims: cloneClaims(claims), expires: expiry, ctx: ctx, cancel: cancel}
	s.entries[hash] = entry
	s.leases[leaseID] = hash
	entry.timer = s.clock.AfterFunc(expiry.Sub(now), func() { s.expire(hash, entry) })
	return IssuedCredential{secret: &credentialSecret{token: base64.RawURLEncoding.EncodeToString(secret[:])}, LeaseID: leaseID, ExpiresAt: expiry}, nil
}
func (s *CredentialStore) expire(hash [32]byte, entry *credentialEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[hash] == entry {
		// A clock implementation must schedule callbacks no earlier than requested.
		s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
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
	// The hash comparison and active-generation check are defense in depth:
	// exact indexing and eager generation revocation already enforce them.
	if !ok || entry == nil || subtle.ConstantTimeCompare(hash[:], entry.hash[:]) != 1 {
		return hash, nil, refusal(capability.Unauthenticated, "")
	}
	if s.closed || !s.clock.Now().Round(0).Before(entry.expires) || s.active[entry.claims.Owner.OwnerID] != entry.claims.Owner.OwnerGeneration {
		s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
		return hash, nil, refusal(capability.Unauthenticated, "")
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
	LeaseID   string
}

func (s *CredentialStore) Verify(token string, subject Subject, owner capability.RuntimeIdentity, audience string) (CredentialLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, entry, err := s.findLocked(token)
	if err != nil {
		return CredentialLease{}, err
	}
	if entry.claims.Subject != subject || entry.claims.Owner != owner || entry.claims.Audience != audience {
		return CredentialLease{}, refusal(capability.Unauthenticated, "")
	}
	return CredentialLease{Claims: cloneClaims(entry.claims), ExpiresAt: entry.expires, Context: entry.ctx, LeaseID: entry.leaseID}, nil
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
		return IssuedCredential{}, refusal(capability.Unauthenticated, "")
	}
	for _, id := range next.GrantIDs {
		// Subset validation is defense in depth: capability-name equality also
		// rejects an ID missing from the original claim map.
		if !slices.Contains(old.claims.GrantIDs, id) {
			return IssuedCredential{}, refusal(capability.ScopeDenied, next.CapabilityNames[id])
		}
		if next.CapabilityNames[id] != old.claims.CapabilityNames[id] {
			return IssuedCredential{}, refusal(capability.ScopeDenied, next.CapabilityNames[id])
		}
		if err := capability.CheckNarrowing(old.claims.CapabilityNames[id], old.claims.Scopes[id], next.Scopes[id]); err != nil {
			return IssuedCredential{}, err
		}
	}
	replacement, err := s.issueLocked(next, lease, old.leaseID, old.lifetimeEnd)
	if err != nil {
		return IssuedCredential{}, err
	}
	s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
	return replacement, nil
}
func (s *CredentialStore) removeLocked(hash [32]byte, cause error) {
	if entry := s.entries[hash]; entry != nil {
		delete(s.entries, hash)
		if s.leases[entry.leaseID] == hash {
			delete(s.leases, entry.leaseID)
		}
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
			s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
		}
	}
}

// RevokeOwner fences exactly one incarnation; stale cleanup cannot revoke its
// replacement. Use this for lifecycle stop/disable/unload; client or grant
// withdrawal uses the selective revocation methods below.
func (s *CredentialStore) RevokeOwner(tuple capability.RuntimeIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tuple.Validate() != nil || tuple.HostInstance != s.host {
		return
	}
	// Stop can win a race with activation; retain the fence even when inactive.
	s.generations[tuple.OwnerID] = max(s.generations[tuple.OwnerID], tuple.OwnerGeneration)
	if s.active[tuple.OwnerID] == tuple.OwnerGeneration {
		s.revokeOwnerLocked(tuple.OwnerID)
	}
}

// RevokeLease withdraws the current token in a renewal chain by non-secret ID.
func (s *CredentialStore) RevokeLease(leaseID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hash, ok := s.leases[leaseID]; ok {
		s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
	}
}

// RevokeSubject withdraws one verified client without fencing the incarnation.
func (s *CredentialStore) RevokeSubject(owner capability.RuntimeIdentity, subject Subject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, entry := range s.entries {
		if entry.claims.Owner == owner && entry.claims.Subject == subject {
			s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
		}
	}
}

// RevokeGrant withdraws credentials referencing a specific grant in one tuple.
func (s *CredentialStore) RevokeGrant(owner capability.RuntimeIdentity, grantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, entry := range s.entries {
		if entry.claims.Owner == owner && slices.Contains(entry.claims.GrantIDs, grantID) {
			s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
		}
	}
}
func (s *CredentialStore) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hash, ok := tokenHash(token); ok {
		s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
	}
}
func (s *CredentialStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for hash := range s.entries {
		s.removeLocked(hash, refusal(capability.Unauthenticated, ""))
	}
	// Clear is defense in depth: closed already refuses every operation.
	clear(s.active)
}
func cloneScope(s capability.Scope) capability.Scope {
	out := capability.Scope{Allowlists: map[string][]string{}, Limits: map[string]int64{}}
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
	scopes := map[string]capability.Scope{}
	for k, v := range c.Scopes {
		scopes[k] = cloneScope(v)
	}
	c.Scopes = scopes
	names := map[string]string{}
	for id, name := range c.CapabilityNames {
		names[id] = name
	}
	c.CapabilityNames = names
	expires := map[string]time.Time{}
	for id, expiry := range c.GrantExpiresAt {
		expires[id] = expiry.Round(0)
	}
	c.GrantExpiresAt = expires
	return c
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
