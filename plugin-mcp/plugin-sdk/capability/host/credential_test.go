package host

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
)

type fakeTimer struct {
	mu       sync.Mutex
	stopped  bool
	due      time.Time
	callback func()
}

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	previous := !t.stopped
	t.stopped = true
	return previous
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{due: c.now.Add(d), callback: f}
	c.timers = append(c.timers, timer)
	return timer
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	timers := append([]*fakeTimer(nil), c.timers...)
	c.mu.Unlock()
	for _, timer := range timers {
		timer.mu.Lock()
		fire := !timer.stopped && !now.Before(timer.due)
		if fire {
			timer.stopped = true
		}
		timer.mu.Unlock()
		if fire {
			timer.callback()
		}
	}
}
func credentialFixture(t *testing.T) (*CredentialStore, *fakeClock, CredentialClaims) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	store, err := NewCredentialStore(CredentialConfig{HostInstance: "epoch", Audience: "bridge", Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	claims := CredentialClaims{Subject: Subject{SessionClient, "session"}, Owner: capability.RuntimeIdentity{HostInstance: "epoch", OwnerID: "owner", OwnerGeneration: 1}, Audience: "bridge", GrantIDs: []string{"grant"}, CapabilityNames: map[string]string{"grant": capability.ReadonlyQuery}, GrantExpiresAt: map[string]time.Time{"grant": clock.Now().Add(20 * time.Minute)}, Scopes: map[string]capability.Scope{"grant": {Allowlists: map[string][]string{"targets": {"one"}}, Limits: map[string]int64{"bytes": 100}}}}
	if err := store.ActivateOwner(claims.Owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store, clock, claims
}
func verifyClaims(store *CredentialStore, token string, c CredentialClaims) (CredentialLease, error) {
	return store.Verify(token, c.Subject, c.Owner, c.Audience)
}
func unauthenticated(t *testing.T, err error) {
	t.Helper()
	var failure *capability.Error
	if !errors.As(err, &failure) || failure.Code != capability.Unauthenticated {
		t.Fatalf("expected unauthenticated, got %v", err)
	}
}
func TestCredentialIssuanceAndHashOnlyStorage(t *testing.T) {
	store, clock, claims := credentialFixture(t)
	issued, err := issueForTest(t, store, claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(issued.Token)
	if err != nil || len(raw) != 32 {
		t.Fatal("not 256 random bits")
	}
	if !issued.ExpiresAt.Equal(clock.Now().Add(5 * time.Minute)) {
		t.Fatal("wrong default expiry")
	}
	hash := sha256.Sum256(raw)
	if len(store.entries) != 1 || store.entries[hash] == nil {
		t.Fatal("not SHA-256 indexed")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if strings.Contains(fmt.Sprintf(format, issued.Credential), issued.Token) {
			t.Fatal("formatted token leak")
		}
	}
	lease, err := verifyClaims(store, issued.Token, claims)
	if err != nil {
		t.Fatal(err)
	}
	claims.Scopes["grant"].Allowlists["targets"][0] = "other"
	lease.Claims.Scopes["grant"].Limits["bytes"] = 999
	again, err := verifyClaims(store, issued.Token, claims)
	if err != nil || again.Claims.Scopes["grant"].Limits["bytes"] != 100 || again.Claims.Scopes["grant"].Allowlists["targets"][0] != "one" {
		t.Fatal("claims snapshot aliases store")
	}
}
func TestWrongCredentialIdentityAndPluginRefusal(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	for _, mutate := range []func(*CredentialClaims){
		func(c *CredentialClaims) { c.Subject.ID = "forged" },
		func(c *CredentialClaims) { c.Subject.Kind = MCPProxyClient },
		func(c *CredentialClaims) { c.Owner.OwnerGeneration++ },
		func(c *CredentialClaims) { c.Owner.HostInstance = "other" },
		func(c *CredentialClaims) { c.Audience = "other" },
	} {
		candidate := cloneClaims(claims)
		mutate(&candidate)
		_, err := verifyClaims(store, issued.Token, candidate)
		unauthenticated(t, err)
	}
	claims.Subject.Kind = "plugin"
	_, err := issueForTest(t, store, claims, 0)
	unauthenticated(t, err)
	for _, token := range []string{"", issued.Token + "=", strings.Repeat("!", 43), strings.Repeat("A", 43)} {
		_, err := verifyClaims(store, token, claims)
		unauthenticated(t, err)
	}
}
func TestExpiryAndGenerationReplacementCancelLeases(t *testing.T) {
	store, clock, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, time.Second)
	lease, _ := verifyClaims(store, issued.Token, claims)
	clock.Advance(time.Second)
	if context.Cause(lease.Context) == nil {
		t.Fatal("expiry did not cancel lease without a verify call")
	}
	_, err := verifyClaims(store, issued.Token, claims)
	unauthenticated(t, err)
	issued, _ = issueForTest(t, store, claims, 0)
	lease, _ = verifyClaims(store, issued.Token, claims)
	replacement := claims.Owner
	replacement.OwnerGeneration++
	if err := store.ActivateOwner(replacement); err != nil {
		t.Fatal(err)
	}
	if context.Cause(lease.Context) == nil {
		t.Fatal("replacement did not cancel")
	}
	_, err = verifyClaims(store, issued.Token, claims)
	unauthenticated(t, err)
	if err := store.ActivateOwner(claims.Owner); err == nil {
		t.Fatal("generation counter reset accepted")
	}
	nextClaims := cloneClaims(claims)
	nextClaims.Owner = replacement
	next, _ := issueForTest(t, store, nextClaims, 0)
	store.RevokeOwner(claims.Owner)
	if _, err := verifyClaims(store, next.Token, nextClaims); err != nil {
		t.Fatal("stale cleanup fenced replacement")
	}
	store.RevokeOwner(replacement)
	_, err = verifyClaims(store, next.Token, nextClaims)
	unauthenticated(t, err)
	if err := store.ActivateOwner(replacement); err == nil {
		t.Fatal("fenced generation resurrected")
	}
}
func TestCredentialRenewalOnlyNarrowsAndRotates(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	lease, _ := verifyClaims(store, issued.Token, claims)
	for _, mutate := range []func(*CredentialClaims){
		func(c *CredentialClaims) { c.Scopes["grant"].Limits["bytes"] = 101 },
		func(c *CredentialClaims) { c.Scopes["grant"].Allowlists["targets"] = []string{"other"} },
		func(c *CredentialClaims) {
			c.GrantIDs = append(c.GrantIDs, "another")
			c.Scopes["another"] = capability.Scope{}
			c.CapabilityNames["another"] = capability.StorageRead
			c.GrantExpiresAt["another"] = c.GrantExpiresAt["grant"]
		},
	} {
		candidate := cloneClaims(claims)
		mutate(&candidate)
		_, err := renewForTest(t, store, issued.Token, candidate, 0)
		var e *capability.Error
		if !errors.As(err, &e) || e.Code != capability.ScopeDenied {
			t.Fatalf("widening accepted: %v", err)
		}
	}
	next := cloneClaims(claims)
	next.Scopes["grant"].Limits["bytes"] = 50
	renewed, err := renewForTest(t, store, issued.Token, next, 0)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Token == issued.Token {
		t.Fatal("token did not rotate")
	}
	if context.Cause(lease.Context) == nil {
		t.Fatal("old lease survives renewal")
	}
	_, err = verifyClaims(store, issued.Token, claims)
	unauthenticated(t, err)
	if _, err := verifyClaims(store, renewed.Token, next); err != nil {
		t.Fatal(err)
	}
}
func TestRevocationShutdownAndLeaseBounds(t *testing.T) {
	store, _, claims := credentialFixture(t)
	for _, lease := range []time.Duration{-1, DefaultCredentialLease + 1} {
		if _, err := issueForTest(t, store, claims, lease); err == nil {
			t.Fatal("invalid lease accepted")
		}
	}
	issued, _ := issueForTest(t, store, claims, 0)
	lease, _ := verifyClaims(store, issued.Token, claims)
	store.Revoke(issued.Token)
	if context.Cause(lease.Context) == nil {
		t.Fatal("revocation did not cancel")
	}
	issued, _ = issueForTest(t, store, claims, 0)
	lease, _ = verifyClaims(store, issued.Token, claims)
	store.Close()
	if context.Cause(lease.Context) == nil {
		t.Fatal("shutdown did not cancel")
	}
	_, err := issueForTest(t, store, claims, 0)
	unauthenticated(t, err)
}
func TestConcurrentCredentialFencing(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 30 {
				verifyClaims(store, issued.Token, claims)
			}
		})
	}
	wg.Go(func() { store.RevokeOwner(claims.Owner) })
	wg.Wait()
	_, err := verifyClaims(store, issued.Token, claims)
	unauthenticated(t, err)
}

func TestIssueClaimsAndRenewalIdentityAreClosed(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	for _, mutate := range []func(*CredentialClaims){
		func(c *CredentialClaims) { c.Audience = "other" },
		func(c *CredentialClaims) { c.Owner.HostInstance = "other" },
		func(c *CredentialClaims) { c.Owner.OwnerGeneration = 2 },
		func(c *CredentialClaims) {
			c.GrantIDs = nil
			c.Scopes = nil
			c.CapabilityNames = nil
			c.GrantExpiresAt = nil
		},
		func(c *CredentialClaims) { delete(c.Scopes, "grant") },
		func(c *CredentialClaims) { delete(c.CapabilityNames, "grant") },
		func(c *CredentialClaims) { c.GrantIDs = append(c.GrantIDs, "grant") },
		func(c *CredentialClaims) { c.Scopes["grant"].Limits["bytes"] = -1 },
	} {
		candidate := cloneClaims(claims)
		mutate(&candidate)
		if _, err := issueForTest(t, store, candidate, 0); err == nil {
			t.Fatal("invalid claims issued")
		}
	}
	changed := cloneClaims(claims)
	changed.Subject.ID = "other"
	if _, err := renewForTest(t, store, issued.Token, changed, 0); err == nil {
		t.Fatal("subject changed on renewal")
	}
	changed = cloneClaims(claims)
	changed.CapabilityNames["grant"] = capability.StorageRead
	if _, err := renewForTest(t, store, issued.Token, changed, 0); err == nil {
		t.Fatal("capability changed on renewal")
	}
	changed = cloneClaims(claims)
	changed.Scopes["grant"].Limits["bytes"] = 999
	if _, err := renewForTest(t, store, issued.Token, changed, 0); err == nil {
		t.Fatal("widening accepted")
	}
	if _, err := verifyClaims(store, issued.Token, claims); err != nil {
		t.Fatal("failed renewal revoked original")
	}
}
func TestVerificationExpiryWithoutTimerCallback(t *testing.T) {
	store, clock, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	lease, _ := verifyClaims(store, issued.Token, claims)
	clock.mu.Lock()
	clock.now = issued.ExpiresAt
	clock.mu.Unlock()
	_, err := verifyClaims(store, issued.Token, claims)
	unauthenticated(t, err)
	if context.Cause(lease.Context) == nil {
		t.Fatal("verify did not cancel expired lease")
	}
}
func TestLeaseMaximumIsConfigurable(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	store, err := NewCredentialStore(CredentialConfig{HostInstance: "epoch", Audience: "bridge", MaxLease: time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, claims := credentialFixture(t)
	store.ActivateOwner(claims.Owner)
	issued, err := issueForTest(t, store, claims, 0)
	if err != nil || !issued.ExpiresAt.Equal(clock.Now().Add(time.Second)) {
		t.Fatal("default exceeds configured maximum")
	}
	if _, err := issueForTest(t, store, claims, 2*time.Second); err == nil {
		t.Fatal("maximum ignored")
	}
}

type testCredential struct {
	Credential     IssuedCredential
	Token, LeaseID string
	ExpiresAt      time.Time
}

func captureCredential(t *testing.T, c IssuedCredential, err error) (testCredential, error) {
	t.Helper()
	if err != nil {
		return testCredential{}, err
	}
	token, revealErr := c.Reveal()
	if revealErr != nil {
		t.Fatal(revealErr)
	}
	return testCredential{c, token, c.LeaseID, c.ExpiresAt}, nil
}
func issueForTest(t *testing.T, store *CredentialStore, claims CredentialClaims, lease time.Duration) (testCredential, error) {
	t.Helper()
	c, err := store.Issue(claims, lease)
	return captureCredential(t, c, err)
}
func renewForTest(t *testing.T, store *CredentialStore, token string, claims CredentialClaims, lease time.Duration) (testCredential, error) {
	t.Helper()
	c, err := store.Renew(token, claims, lease)
	return captureCredential(t, c, err)
}
