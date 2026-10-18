package pairing

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/security/auth"
	_ "modernc.org/sqlite"
)

func fixture(t *testing.T) (*Store, *sql.DB, *time.Time) {
	t.Helper()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "pairing.db"))
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, e := Open(db, Config{Audience: "example", Scopes: []string{"example.read", "example.write"}, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	return s, db, &now
}
func grant(t *testing.T, s *Store) Grant {
	t.Helper()
	g, e := s.CreateGrant(t.Context(), "owner", "Laptop", []string{"example.read", "example.write"})
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func TestExchangeNarrowingReplayAndRevocation(t *testing.T) {
	s, db, now := fixture(t)
	g := grant(t, s)
	for _, bad := range []ExchangeRequest{{Code: g.Code, Audience: "other"}, {Code: g.Code, Audience: "example", Scopes: []string{"example.admin"}}, {Code: g.Code, Audience: "example", Scopes: []string{}}} {
		if _, e := s.Exchange(t.Context(), bad); !errors.Is(e, ErrGrant) {
			t.Fatal("accepted invalid exchange", e)
		}
	}
	x, e := s.Exchange(t.Context(), ExchangeRequest{Code: g.Code, Audience: "example", Scopes: []string{"example.read"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Exchange(t.Context(), ExchangeRequest{Code: g.Code, Audience: "example"}); !errors.Is(e, ErrGrant) {
		t.Fatal("replayed grant", e)
	}
	p, e := s.Verify(t.Context(), x.Token)
	if e != nil || !p.Verified || p.Kind != auth.KindDevice || !p.HasScope("example.read") || p.HasScope("example.write") {
		t.Fatal("wrong principal", p, e)
	}
	var hash string
	if e = db.QueryRow("SELECT token_hash FROM hollis_pairing_devices WHERE id=?", x.Device.ID).Scan(&hash); e != nil || hash != auth.HashKey(x.Token) || hash == x.Token {
		t.Fatal("credential storage", e)
	}
	*now = now.Add(time.Hour)
	if _, e = s.Renew(t.Context(), x.Token, []string{"example.write"}); !errors.Is(e, auth.ErrInvalidToken) {
		t.Fatal("renew widened scopes", e)
	}
	d, e := s.Renew(t.Context(), x.Token, nil)
	if e != nil || !d.ExpiresAt.After(x.Device.ExpiresAt) {
		t.Fatal("renew failed", e)
	}
	if e = s.Revoke(t.Context(), "wrong-owner", d.ID); !errors.Is(e, auth.ErrInvalidToken) {
		t.Fatal("other owner revoked device", e)
	}
	if e = s.Revoke(t.Context(), "owner", d.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(t.Context(), x.Token); !errors.Is(e, auth.ErrInvalidToken) {
		t.Fatal("revoked token accepted", e)
	}
	if _, e = s.Renew(t.Context(), x.Token, nil); !errors.Is(e, auth.ErrInvalidToken) {
		t.Fatal("revoked token renewed", e)
	}
}
func TestExpiredAndRevokedGrants(t *testing.T) {
	s, _, now := fixture(t)
	expired := grant(t, s)
	*now = now.Add(5 * time.Minute)
	if _, e := s.Exchange(t.Context(), ExchangeRequest{Code: expired.Code, Audience: "example"}); !errors.Is(e, ErrGrant) {
		t.Fatal("expiry boundary accepted", e)
	}
	g := grant(t, s)
	if e := s.RevokeGrant(t.Context(), "owner", g.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Exchange(t.Context(), ExchangeRequest{Code: g.Code, Audience: "example"}); !errors.Is(e, ErrGrant) {
		t.Fatal("revoked grant accepted", e)
	}
	g = grant(t, s)
	x, e := s.Exchange(t.Context(), ExchangeRequest{Code: g.Code, Audience: "example"})
	if e != nil {
		t.Fatal(e)
	}
	*now = x.Device.ExpiresAt
	if _, e = s.Verify(t.Context(), x.Token); !errors.Is(e, auth.ErrInvalidToken) {
		t.Fatal("expired device accepted", e)
	}
}
func TestConcurrentConsumeAndRollback(t *testing.T) {
	s, db, _ := fixture(t)
	g := grant(t, s)
	if _, e := db.Exec(`CREATE TRIGGER fail_mint BEFORE INSERT ON hollis_pairing_devices BEGIN SELECT RAISE(ABORT,'test mint failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Exchange(t.Context(), ExchangeRequest{Code: g.Code, Audience: "example"}); e == nil {
		t.Fatal("mint failure ignored")
	}
	if _, e := db.Exec("DROP TRIGGER fail_mint"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, e := s.Exchange(context.Background(), ExchangeRequest{Code: g.Code, Audience: "example"})
			results <- e
		})
	}
	wg.Wait()
	close(results)
	success, replay := false, false
	for e := range results {
		if e == nil {
			if success {
				t.Fatal("grant used twice")
			}
			success = true
		} else if errors.Is(e, ErrGrant) {
			replay = true
		} else {
			t.Fatal(e)
		}
	}
	if !success || !replay {
		t.Fatal("rollback spent grant or race lost refusal")
	}
}
func TestOriginBoundary(t *testing.T) {
	for _, raw := range []string{"https://example.test", "http://localhost:8080/", "http://[::1]:8080"} {
		if _, e := Origin(raw); e != nil {
			t.Fatal(raw, e)
		}
	}
	for _, raw := range []string{"http://example.test", "http://localhost.evil.test", "https://user:secret@example.test", "https://example.test/path", "https://example.test?token=secret", "https://example.test/#secret", "https://:80"} {
		if _, e := Origin(raw); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	s, _, _ := fixture(t)
	if _, e := s.CreateGrant(t.Context(), "owner", "Laptop", []string{"*"}); !errors.Is(e, ErrInput) {
		t.Fatal("wildcard grant", e)
	}
	if _, e := s.CreateGrant(t.Context(), "owner", strings.Repeat("x", 129), []string{"example.read"}); !errors.Is(e, ErrInput) {
		t.Fatal("unbounded label", e)
	}
}
