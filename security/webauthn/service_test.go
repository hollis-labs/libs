package webauthn_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	proof "github.com/hollis-labs/libs/security/webauthn"
	"github.com/hollis-labs/libs/security/webauthn/presencetest"
	"github.com/hollis-labs/libs/security/webauthn/sqlitestore"
	_ "modernc.org/sqlite"
)

const origin = "https://example.test"

var ctx = context.Background()

type fixture struct {
	db    *sql.DB
	store *sqlitestore.Store
	svc   *proof.Service
	party *proof.Party
	now   time.Time
	key   *presencetest.Authenticator
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "public-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	st, err := sqlitestore.New(ctx, db, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"alice", "bob"} {
		if err = st.CompareAndSwap(ctx, subject, 0, proof.Snapshot{User: proof.User{ID: []byte(subject), Name: subject, DisplayName: subject}}); err != nil {
			t.Fatal(err)
		}
	}
	party, err := proof.NewParty("example.test", "Example", []string{origin})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, store: st, party: party, now: time.Now().UTC(), key: presencetest.New(t, "example.test", origin)}
	f.svc, err = proof.NewService(party, st, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	id, opts, err := f.svc.BeginRegistration(ctx, "alice", "session-a", origin, f.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishRegistration(ctx, "alice", "session-a", id, f.key.Register(t, opts)); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) begin(t *testing.T) (string, json.RawMessage, []byte) {
	t.Helper()
	ch := proof.Challenge([]byte("operation"), []byte(t.Name()), []byte(rand.Text()))
	id, opts, err := f.svc.BeginAssertion(ctx, "alice", "session-a", ch, origin, f.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return id, opts, ch
}
func TestProofSurvivesRestartButCannotBeDecidedTwice(t *testing.T) {
	f := setup(t)
	id, opts, ch := f.begin(t)
	raw := f.key.Assert(t, opts)
	// New service has no process-local pending ceremony cache.
	other, err := proof.NewService(f.party, f.store, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.FinishAssertion(ctx, "alice", "session-a", id, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishAssertion(ctx, "alice", "session-a", id, raw); !errors.Is(err, proof.ErrCeremony) {
		t.Fatalf("replay: %v", err)
	}
	if _, err = other.Reverify(ctx, "alice", id, ch); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Reverify(ctx, "bob", id, ch); err == nil {
		t.Fatal("cross-subject proof accepted")
	}
	if _, err = other.Reverify(ctx, "alice", id, []byte("changed operation")); err == nil {
		t.Fatal("changed operation accepted")
	}
	f.now = f.now.Add(time.Minute)
	if _, err = other.Reverify(ctx, "alice", id, ch); !errors.Is(err, proof.ErrCeremony) {
		t.Fatalf("expiry boundary: %v", err)
	}
}
func TestAssertionFailuresConsumeCeremony(t *testing.T) {
	for _, tc := range []string{"origin", "rp", "uv", "up", "unknown", "signature", "counter", "challenge"} {
		t.Run(tc, func(t *testing.T) {
			f := setup(t)
			if tc == "counter" {
				id, opts, _ := f.begin(t)
				if _, err := f.svc.FinishAssertion(ctx, "alice", "session-a", id, f.key.Assert(t, opts)); err != nil {
					t.Fatal(err)
				}
				f.key.Counter = 0
			}
			id, opts, _ := f.begin(t)
			key := f.key
			switch tc {
			case "origin":
				key.Origin = "https://wrong.test"
			case "rp":
				key.RPID = "wrong.test"
			case "uv":
				key.UV = false
			case "up":
				key.Present = false
			case "unknown":
				key = presencetest.New(t, "example.test", origin)
			case "challenge":
				opts = json.RawMessage(`{"publicKey":{"challenge":"aW52YWxpZA"}}`)
			}
			raw := key.Assert(t, opts)
			if tc == "signature" {
				var data map[string]any
				if err := json.Unmarshal(raw, &data); err != nil {
					t.Fatal(err)
				}
				data["response"].(map[string]any)["signature"] = "AA"
				raw, _ = json.Marshal(data)
			}
			if _, err := f.svc.FinishAssertion(ctx, "alice", "session-a", id, raw); err == nil {
				t.Fatal("invalid assertion accepted")
			}
			if _, err := f.svc.FinishAssertion(ctx, "alice", "session-a", id, raw); !errors.Is(err, proof.ErrCeremony) {
				t.Fatalf("failed ceremony replay: %v", err)
			}
		})
	}
}
func TestRemovalInvalidatesRecordedProof(t *testing.T) {
	f := setup(t)
	id, opts, ch := f.begin(t)
	p, err := f.svc.FinishAssertion(ctx, "alice", "session-a", id, f.key.Assert(t, opts))
	if err != nil {
		t.Fatal(err)
	}
	removal, ropts, err := f.svc.BeginRemoval(ctx, "alice", "session-a", p.CredentialID, origin, f.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.FinishRemoval(ctx, "alice", "session-a", removal, f.key.Assert(t, ropts)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Reverify(ctx, "alice", id, ch); err == nil {
		t.Fatalf("removed key: %v", err)
	}
	if err = f.svc.FinishRemoval(ctx, "alice", "session-a", removal, f.key.Assert(t, ropts)); !errors.Is(err, proof.ErrCeremony) {
		t.Fatalf("removal replay: %v", err)
	}
}
func TestConcurrentCompletionAcceptsOnce(t *testing.T) {
	f := setup(t)
	id, opts, _ := f.begin(t)
	raw := f.key.Assert(t, opts)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.FinishAssertion(ctx, "alice", "session-a", id, raw)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d completions", accepted)
	}
}
func TestStoreRejectsStaleCounterAndCrossSubjectCredential(t *testing.T) {
	f := setup(t)
	alice, err := f.store.Load(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	stale := alice
	alice.User.Credentials[0].Authenticator.SignCount = 10
	if err = f.store.CompareAndSwap(ctx, "alice", alice.Version, alice); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CompareAndSwap(ctx, "alice", stale.Version, stale); !errors.Is(err, proof.ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	bob, err := f.store.Load(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	bob.User.Credentials = alice.User.Credentials
	if err = f.store.CompareAndSwap(ctx, "bob", bob.Version, bob); !errors.Is(err, proof.ErrConflict) {
		t.Fatalf("credential stolen: %v", err)
	}
	got, err := f.store.Load(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.User.Credentials) != 0 {
		t.Fatal("failed transaction changed bob")
	}
}

type failingStore struct {
	proof.Store
	fail bool
}

func (s *failingStore) CompareAndSwap(ctx context.Context, id string, v int64, n proof.Snapshot) error {
	if s.fail {
		return errors.New("synthetic write failure")
	}
	return s.Store.CompareAndSwap(ctx, id, v, n)
}
func TestWriteFailureDoesNotReturnProof(t *testing.T) {
	f := setup(t)
	st := &failingStore{Store: f.store}
	svc, err := proof.NewService(f.party, st, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	id, opts, _ := f.begin(t)
	st.fail = true
	p, err := svc.FinishAssertion(ctx, "alice", "session-a", id, f.key.Assert(t, opts))
	if err == nil || len(p.Assertion) != 0 {
		t.Fatal("proof escaped failed commit")
	}
	loaded, err := f.store.Load(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.User.Credentials[0].Authenticator.SignCount != 0 {
		t.Fatal("counter changed despite failure")
	}
}

func TestPendingAndCounterStateSurviveDatabaseReopen(t *testing.T) {
	f := setup(t)
	id, opts, ch := f.begin(t)
	var path string
	var seq int
	var name string
	if err := f.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := sqlitestore.New(ctx, db, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := proof.NewService(f.party, st, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.FinishAssertion(ctx, "alice", "session-a", id, f.key.Assert(t, opts)); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Reverify(ctx, "alice", id, ch); err != nil {
		t.Fatal(err)
	}
	stored, err := st.Load(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if stored.User.Credentials[0].Authenticator.SignCount != 1 {
		t.Fatal("counter was not persisted")
	}
}
func TestCounterlessAssertionStillSingleUse(t *testing.T) {
	f := setup(t)
	id, opts, _ := f.begin(t)
	f.key.Counter = ^uint32(0) // synthetic authenticator wraps to the zero-count format
	raw := f.key.Assert(t, opts)
	if _, err := f.svc.FinishAssertion(ctx, "alice", "session-a", id, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.FinishAssertion(ctx, "alice", "session-a", id, raw); !errors.Is(err, proof.ErrCeremony) {
		t.Fatalf("counterless replay: %v", err)
	}
}
