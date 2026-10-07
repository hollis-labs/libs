package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
)

func TestSecretRevealOnceAndRedactedSurfaces(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, err := store.Issue(claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	copy := issued
	type private struct{ credential IssuedCredential }
	type public struct{ Credential IssuedCredential }
	var outputs []string
	// Collect every formatting path before Reveal clears the captured token.
	for _, value := range []any{issued, &issued, private{issued}, &private{issued}, public{issued}, []IssuedCredential{issued}, map[string]IssuedCredential{"credential": issued}} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%e", "%E", "%f", "%g", "%G", "%t", "%c", "%U", "%b", "%o", "%O", "%p", "%T"} {
			outputs = append(outputs, fmt.Sprintf(format, value))
		}
	}
	secret, err := issued.Reveal()
	if err != nil || len(secret) != 43 {
		t.Fatal("first reveal failed")
	}
	for _, output := range outputs {
		if strings.Contains(output, secret) {
			t.Fatal("formatted secret exposed before Reveal")
		}
	}
	if _, err := copy.Reveal(); err == nil {
		t.Fatal("copy revealed secret twice")
	}
	// Check before Reveal too: JSON/log methods must not consume/reveal it.
	hidden, err := store.Issue(claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(hidden)
	if err != nil {
		t.Fatal(err)
	}
	text, err := hidden.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var jsonLog, textLog bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonLog, nil)).Info("issued", "credential", hidden)
	slog.New(slog.NewTextHandler(&textLog, nil)).Info("issued", "credential", hidden)
	formatted := fmt.Sprintf("%+v", private{hidden})
	secret, err = hidden.Reveal()
	if err != nil {
		t.Fatal("redaction consumed secret")
	}
	for _, output := range []string{string(raw), string(text), jsonLog.String(), textLog.String(), formatted} {
		if strings.Contains(output, secret) {
			t.Fatal("secret leaked to JSON/text/log/nested formatting")
		}
	}
	if _, err := verifyClaims(store, secret, claims); err != nil {
		t.Fatal(err)
	}
}
func TestStableLeaseRevocationAcrossRenewal(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	renewed, err := renewForTest(t, store, issued.Token, claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	if issued.LeaseID == "" || issued.LeaseID != renewed.LeaseID {
		t.Fatal("lease handle not stable")
	}
	lease, _ := verifyClaims(store, renewed.Token, claims)
	store.RevokeLease(issued.LeaseID)
	_, err = verifyClaims(store, renewed.Token, claims)
	unauthenticated(t, err)
	if context.Cause(lease.Context) == nil {
		t.Fatal("lease revocation did not cancel successor")
	}
}
func TestSelectiveSubjectAndGrantRevocation(t *testing.T) {
	store, _, claims := credentialFixture(t)
	one, _ := issueForTest(t, store, claims, 0)
	other := cloneClaims(claims)
	other.Subject = Subject{MCPProxyClient, "other"}
	two, _ := issueForTest(t, store, other, 0)
	store.RevokeSubject(claims.Owner, claims.Subject)
	_, err := verifyClaims(store, one.Token, claims)
	unauthenticated(t, err)
	if _, err := verifyClaims(store, two.Token, other); err != nil {
		t.Fatal("subject withdrawal affected unrelated client")
	}
	if _, err := issueForTest(t, store, other, 0); err != nil {
		t.Fatal("subject withdrawal fenced owner")
	}
	thirdClaims := cloneClaims(other)
	thirdClaims.GrantIDs = []string{"different"}
	thirdClaims.Scopes = map[string]capability.Scope{"different": claims.Scopes["grant"]}
	thirdClaims.CapabilityNames = map[string]string{"different": capability.ReadonlyQuery}
	thirdClaims.GrantExpiresAt = map[string]time.Time{"different": claims.GrantExpiresAt["grant"]}
	third, _ := issueForTest(t, store, thirdClaims, 0)
	store.RevokeGrant(claims.Owner, "grant")
	_, err = verifyClaims(store, two.Token, other)
	unauthenticated(t, err)
	if _, err := verifyClaims(store, third.Token, thirdClaims); err != nil {
		t.Fatal("grant withdrawal affected unrelated credential")
	}
}
func TestStopBeforeActivationAndWrongEpoch(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	future := claims.Owner
	future.OwnerGeneration = 2
	store.RevokeOwner(future)
	if err := store.ActivateOwner(future); err == nil {
		t.Fatal("stopped generation activated later")
	}
	if _, err := verifyClaims(store, issued.Token, claims); err != nil {
		t.Fatal("future cleanup fenced current generation")
	}
	wrong := claims.Owner
	wrong.HostInstance = "other"
	wrong.OwnerGeneration = 100
	store.RevokeOwner(wrong)
	if _, err := verifyClaims(store, issued.Token, claims); err != nil {
		t.Fatal("wrong epoch revoked current token")
	}
	next := claims.Owner
	next.OwnerGeneration = 3
	if err := store.ActivateOwner(next); err != nil {
		t.Fatal("wrong epoch poisoned fence")
	}
}
func TestRenewalClaimsExpiredAndClosedStore(t *testing.T) {
	store, clock, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	extra := cloneClaims(claims)
	extra.Scopes["unlisted"] = capability.Scope{Limits: map[string]int64{"bytes": 999999}}
	if _, err := renewForTest(t, store, issued.Token, extra, 0); err == nil {
		t.Fatal("unlisted scope added on renewal")
	}
	clock.mu.Lock()
	clock.now = issued.ExpiresAt
	clock.mu.Unlock()
	if _, err := renewForTest(t, store, issued.Token, claims, 0); err == nil {
		t.Fatal("expired renewal accepted before timer")
	}
	store.Close()
	next := claims.Owner
	next.OwnerGeneration++
	if err := store.ActivateOwner(next); err == nil {
		t.Fatal("closed store activated owner")
	}
	if _, err := issueForTest(t, store, claims, 0); err == nil {
		t.Fatal("closed store issued")
	}
	if _, err := renewForTest(t, store, issued.Token, claims, 0); err == nil {
		t.Fatal("closed store renewed")
	}
}
func TestCanonicalCredentialIdentifiers(t *testing.T) {
	for _, id := range []string{"", "*", " padded", "control\n", "mid\ncontrol"} {
		store, _, claims := credentialFixture(t)
		claims.GrantIDs = []string{id}
		claims.Scopes = map[string]capability.Scope{id: {}}
		claims.CapabilityNames = map[string]string{id: capability.ReadonlyQuery}
		expiry := claims.GrantExpiresAt["grant"]
		claims.GrantExpiresAt = map[string]time.Time{id: expiry}
		if _, err := issueForTest(t, store, claims, 0); err == nil {
			t.Fatalf("invalid grant ID accepted: %q", id)
		}
	}
	for _, name := range []string{"Not A Capability!", "readonly.Query", "host..query", "host.example", "readonly.*"} {
		store, _, claims := credentialFixture(t)
		claims.CapabilityNames["grant"] = name
		if _, err := issueForTest(t, store, claims, 0); err == nil {
			t.Fatalf("invalid capability name accepted: %q", name)
		}
	}
}
func TestStrictTokenSpellingsAndTimerCleanup(t *testing.T) {
	store, clock, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, issued.Token[42])
	alternative := issued.Token[:42] + string(alphabet[last^1])
	for _, token := range []string{alternative, issued.Token + "\n", issued.Token + "=", issued.Token[:42] + "\n"} {
		_, err := verifyClaims(store, token, claims)
		unauthenticated(t, err)
	}
	clock.mu.Lock()
	timer := clock.timers[0]
	clock.mu.Unlock()
	store.RevokeLease(issued.LeaseID)
	timer.mu.Lock()
	stopped := timer.stopped
	timer.mu.Unlock()
	if !stopped {
		t.Fatal("revocation did not stop eager expiry timer")
	}
}
func TestOwnerActivationIsIdempotent(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, _ := issueForTest(t, store, claims, 0)
	lease, _ := verifyClaims(store, issued.Token, claims)
	if err := store.ActivateOwner(claims.Owner); err != nil {
		t.Fatal("repeated activation failed")
	}
	if context.Cause(lease.Context) != nil {
		t.Fatal("repeated activation cancelled current lease")
	}
	if _, err := verifyClaims(store, issued.Token, claims); err != nil {
		t.Fatal("repeated activation revoked token")
	}
}
func TestWallClockJumpAndGrantExpiryBounds(t *testing.T) {
	store, clock, claims := credentialFixture(t)
	claims.GrantExpiresAt["grant"] = clock.Now().Add(time.Minute)
	issued, _ := issueForTest(t, store, claims, 0)
	if !issued.ExpiresAt.Equal(claims.GrantExpiresAt["grant"]) {
		t.Fatal("credential outlives grant")
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(30 * time.Second)
	clock.mu.Unlock()
	next, err := renewForTest(t, store, issued.Token, claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !next.ExpiresAt.Equal(claims.GrantExpiresAt["grant"]) {
		t.Fatal("renewal outlives grant")
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(10 * time.Minute)
	clock.mu.Unlock()
	_, err = verifyClaims(store, next.Token, claims)
	unauthenticated(t, err)
	now := wallClock{}.Now()
	if now != now.Round(0) {
		t.Fatal("default clock retains monotonic reading")
	}
}
func TestRenewalChainHasAbsoluteLifetime(t *testing.T) {
	_, clock, claims := credentialFixture(t)
	claims.GrantExpiresAt["grant"] = clock.Now().Add(time.Hour)
	store, err := NewCredentialStore(CredentialConfig{HostInstance: "epoch", Audience: "bridge", Clock: clock, MaxLifetime: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.ActivateOwner(claims.Owner)
	issued, _ := issueForTest(t, store, claims, 0)
	clock.Advance(4 * time.Minute)
	renewed, err := renewForTest(t, store, issued.Token, claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(4 * time.Minute)
	renewed, err = renewForTest(t, store, renewed.Token, claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(time.Date(2030, 1, 1, 0, 10, 0, 0, time.UTC)) {
		t.Fatal("renewal chain extended absolute lifetime")
	}
	clock.mu.Lock()
	clock.now = renewed.ExpiresAt
	clock.mu.Unlock()
	if _, err := renewForTest(t, store, renewed.Token, claims, 0); err == nil {
		t.Fatal("chain renewed beyond absolute lifetime")
	}
}
func TestAuditDenialCountersSurviveSinkFailure(t *testing.T) {
	auditor := NewAuditor(auditFunc(func(context.Context, AuditEvent) error { return fmt.Errorf("sink unavailable") }))
	event := AuditEvent{Actor: Actor{"plugin", "owner"}, Outcome: capability.CapabilityDenied, Reason: capability.StaleBinding}
	if auditor.Record(context.Background(), event) {
		t.Fatal("failure not reported")
	}
	key := DenialKey{capability.CapabilityDenied, capability.StaleBinding}
	counts := auditor.Denials()
	if counts[key] != 1 {
		t.Fatal("denial count lost with sink")
	}
	counts[key] = 0
	if auditor.Denials()[key] != 1 {
		t.Fatal("counter snapshots alias internal state")
	}
}

func TestConcurrentRevealHasOneWinner(t *testing.T) {
	store, _, claims := credentialFixture(t)
	issued, err := store.Issue(claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan bool, 8)
	for range 8 {
		go func() { _, err := issued.Reveal(); results <- err == nil }()
	}
	winners := 0
	for range 8 {
		if <-results {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("shared secret revealed more than once")
	}
}

func TestRenewalCannotExtendGrantExpiry(t *testing.T) {
	store, clock, claims := credentialFixture(t)
	claims.GrantExpiresAt["grant"] = clock.Now().Add(2 * time.Minute)
	issued, err := issueForTest(t, store, claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneClaims(claims)
	next.GrantExpiresAt["grant"] = clock.Now().Add(5 * time.Minute)
	if _, err := store.Renew(issued.Token, next, 0); err == nil {
		t.Fatal("grant expiry extended by renewal")
	}
	if _, err := verifyClaims(store, issued.Token, claims); err != nil {
		t.Fatal("refused renewal revoked original")
	}
	next.GrantExpiresAt["grant"] = clock.Now().Add(time.Minute)
	renewed, err := renewForTest(t, store, issued.Token, next, 0)
	if err != nil || !renewed.ExpiresAt.Equal(next.GrantExpiresAt["grant"]) {
		t.Fatal("narrower expiry not honored")
	}
}

func TestStaleRevocationCannotLowerFence(t *testing.T) {
	store, _, claims := credentialFixture(t)
	future := claims.Owner
	future.OwnerGeneration = 3
	store.RevokeOwner(future)
	store.RevokeOwner(claims.Owner)
	if err := store.ActivateOwner(future); err == nil {
		t.Fatal("stale cleanup lowered fence")
	}
	future.OwnerGeneration = 4
	if err := store.ActivateOwner(future); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseIndexRemovalPaths(t *testing.T) {
	paths := map[string]func(*CredentialStore, *fakeClock, CredentialClaims, testCredential){
		"token": func(s *CredentialStore, _ *fakeClock, _ CredentialClaims, c testCredential) { s.Revoke(c.Token) },
		"lease": func(s *CredentialStore, _ *fakeClock, _ CredentialClaims, c testCredential) { s.RevokeLease(c.LeaseID) },
		"subject": func(s *CredentialStore, _ *fakeClock, a CredentialClaims, _ testCredential) {
			s.RevokeSubject(a.Owner, a.Subject)
		},
		"grant": func(s *CredentialStore, _ *fakeClock, a CredentialClaims, _ testCredential) {
			s.RevokeGrant(a.Owner, "grant")
		},
		"owner": func(s *CredentialStore, _ *fakeClock, a CredentialClaims, _ testCredential) { s.RevokeOwner(a.Owner) },
		"replacement": func(s *CredentialStore, _ *fakeClock, a CredentialClaims, _ testCredential) {
			a.Owner.OwnerGeneration++
			if err := s.ActivateOwner(a.Owner); err != nil {
				t.Fatal(err)
			}
		},
		"close": func(s *CredentialStore, _ *fakeClock, _ CredentialClaims, _ testCredential) { s.Close() },
		"timer": func(_ *CredentialStore, clock *fakeClock, _ CredentialClaims, _ testCredential) {
			clock.Advance(DefaultCredentialLease)
		},
		"verify-expiry": func(s *CredentialStore, clock *fakeClock, a CredentialClaims, c testCredential) {
			clock.mu.Lock()
			clock.now = c.ExpiresAt
			clock.mu.Unlock()
			_, err := verifyClaims(s, c.Token, a)
			unauthenticated(t, err)
		},
	}
	for name, remove := range paths {
		t.Run(name, func(t *testing.T) {
			store, clock, claims := credentialFixture(t)
			issued, err := issueForTest(t, store, claims, 0)
			if err != nil {
				t.Fatal(err)
			}
			remove(store, clock, claims, issued)
			store.mu.Lock()
			defer store.mu.Unlock()
			if len(store.entries) != 0 || len(store.leases) != 0 {
				t.Fatal("removed credential retains index state")
			}
		})
	}
	t.Run("renew", func(t *testing.T) {
		store, _, claims := credentialFixture(t)
		issued, _ := issueForTest(t, store, claims, 0)
		renewed, err := renewForTest(t, store, issued.Token, claims, 0)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := verifyClaims(store, renewed.Token, claims)
		if err != nil || lease.LeaseID != issued.LeaseID {
			t.Fatal("renewal lost stable handle")
		}
		store.mu.Lock()
		entries, leases := len(store.entries), len(store.leases)
		store.mu.Unlock()
		if entries != 1 || leases != 1 {
			t.Fatal("renewal retains stale index state")
		}
		store.RevokeLease(issued.LeaseID)
		if len(store.entries) != 0 || len(store.leases) != 0 {
			t.Fatal("successor was not indexed by stable handle")
		}
	})
}

func TestSelectiveRevocationPreservesOtherOwners(t *testing.T) {
	for _, method := range []string{"subject", "grant"} {
		t.Run(method, func(t *testing.T) {
			store, _, claims := credentialFixture(t)
			other := cloneClaims(claims)
			other.Owner.OwnerID = "another"
			if err := store.ActivateOwner(other.Owner); err != nil {
				t.Fatal(err)
			}
			one, _ := issueForTest(t, store, claims, 0)
			two, _ := issueForTest(t, store, other, 0)
			if method == "subject" {
				store.RevokeSubject(claims.Owner, claims.Subject)
			} else {
				store.RevokeGrant(claims.Owner, "grant")
			}
			_, err := verifyClaims(store, one.Token, claims)
			unauthenticated(t, err)
			if _, err := verifyClaims(store, two.Token, other); err != nil {
				t.Fatal("withdrawal crossed owner boundary")
			}
		})
	}
}

func TestCredentialHandleAndExpiryBoundary(t *testing.T) {
	store, clock, claims := credentialFixture(t)
	issued, err := issueForTest(t, store, claims, 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := base64.RawURLEncoding.Strict().DecodeString(issued.LeaseID)
	if err != nil || len(handle) != 16 {
		t.Fatal("handle is not 128 bits")
	}
	lease, err := verifyClaims(store, issued.Token, claims)
	if err != nil || lease.LeaseID != issued.LeaseID {
		t.Fatal("verification lost lease handle")
	}
	clock.mu.Lock()
	clock.now = issued.ExpiresAt
	clock.mu.Unlock()
	_, err = verifyClaims(store, issued.Token, claims)
	unauthenticated(t, err)
}

func TestRevokeOwnerRejectsInvalidIdentity(t *testing.T) {
	for _, mutate := range []func(*capability.RuntimeIdentity){
		func(r *capability.RuntimeIdentity) { r.OwnerGeneration = capability.MaxSafeInteger + 1 },
		func(r *capability.RuntimeIdentity) { r.OwnerGeneration = 0 },
		func(r *capability.RuntimeIdentity) { r.OwnerID = " \n" },
	} {
		t.Run("invalid", func(t *testing.T) {
			store, _, claims := credentialFixture(t)
			invalid := claims.Owner
			mutate(&invalid)
			store.RevokeOwner(invalid)
			if len(store.generations) != 1 || store.generations[claims.Owner.OwnerID] != 1 {
				t.Fatal("invalid identity poisoned fence")
			}
			next := claims.Owner
			next.OwnerGeneration = 2
			if err := store.ActivateOwner(next); err != nil {
				t.Fatal("invalid identity fenced valid activation")
			}
		})
	}
}

func TestSubjectUsesCanonicalIdentifier(t *testing.T) {
	for _, id := range []string{"", "*", " padded", "trailing ", "mid\ncontrol"} {
		store, _, claims := credentialFixture(t)
		claims.Subject.ID = id
		if _, err := store.Issue(claims, 0); err == nil {
			t.Fatalf("invalid subject accepted %q", id)
		}
	}
}

func TestAuditDenialsBoundedWithoutSink(t *testing.T) {
	auditor := NewAuditor(nil)
	for i := 0; i < 100; i++ {
		if !auditor.Record(context.Background(), AuditEvent{Outcome: capability.Code(fmt.Sprint(i)), Reason: capability.FailureDetail(fmt.Sprint(i))}) {
			t.Fatal("missing sink failed")
		}
	}
	auditor.Record(context.Background(), AuditEvent{Outcome: capability.ScopeDenied, Reason: capability.StaleBinding})
	counts := auditor.Denials()
	if len(counts) != 2 || counts[DenialKey{capability.InternalError, ""}] != 100 || counts[DenialKey{capability.ScopeDenied, capability.StaleBinding}] != 1 {
		t.Fatal("denial counters unbounded or lost without sink")
	}
	var seen AuditEvent
	withSink := NewAuditor(auditFunc(func(_ context.Context, e AuditEvent) error { seen = e; return nil }))
	withSink.Record(context.Background(), AuditEvent{Outcome: "unrecognized", Reason: "unrecognized"})
	if seen.Outcome != capability.InternalError || seen.Reason != "" {
		t.Fatal("sink received unnormalized denial")
	}
}
