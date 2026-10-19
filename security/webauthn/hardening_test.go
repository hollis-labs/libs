package webauthn_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	proof "github.com/hollis-labs/libs/security/webauthn"
	"github.com/hollis-labs/libs/security/webauthn/presencetest"
	"github.com/hollis-labs/libs/security/webauthn/sqlitestore"
)

func TestCounterlessChallengeCannotBeReusedAfterPruneAndReopen(t *testing.T) {
	f := setup(t)
	id, opts, ch := f.begin(t)
	f.key.Counter = ^uint32(0)
	if _, err := f.svc.FinishAssertion(ctx, "alice", "session-a", id, f.key.Assert(t, opts)); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(6 * time.Minute)
	f.begin(t) // Prunes the old receipt from the subject snapshot.
	var path, name string
	var seq int
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
	again, options, err := svc.BeginAssertion(ctx, "alice", "session-b", ch, origin, f.now.Add(time.Minute))
	if !errors.Is(err, proof.ErrChallengeReuse) || again != "" || options != nil {
		t.Fatalf("reused challenge: id=%q error=%v", again, err)
	}
}

func TestConcurrentCeremoniesCannotShareChallenge(t *testing.T) {
	f := setup(t)
	ch := proof.Challenge([]byte(t.Name()))
	var wg sync.WaitGroup
	out := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := f.svc.BeginAssertion(ctx, "alice", "session-a", ch, origin, f.now.Add(time.Minute))
			out <- err
		}()
	}
	wg.Wait()
	close(out)
	accepted := 0
	for err := range out {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("same challenge accepted %d times", accepted)
	}
	if _, _, err := f.svc.BeginAssertion(ctx, "alice", "session-b", ch, origin, f.now.Add(time.Minute)); !errors.Is(err, proof.ErrChallengeReuse) {
		t.Fatalf("retry reuse: %v", err)
	}
}

func TestCeremonyCompletionRequiresInitiatingSession(t *testing.T) {
	for _, kind := range []string{"assert", "register", "remove"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			var id string
			var opts, raw json.RawMessage
			var err error
			switch kind {
			case "assert":
				id, opts, _ = f.begin(t)
				raw = f.key.Assert(t, opts)
			case "register":
				id, opts, err = f.svc.BeginRegistration(ctx, "alice", "session-a", origin, f.now.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				raw = presencetest.New(t, "example.test", origin).Register(t, opts)
			case "remove":
				st, e := f.store.Load(ctx, "alice")
				if e != nil {
					t.Fatal(e)
				}
				id, opts, err = f.svc.BeginRemoval(ctx, "alice", "session-a", st.User.Credentials[0].ID, origin, f.now.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				raw = f.key.Assert(t, opts)
			}
			finish := func(binding string) error {
				switch kind {
				case "assert":
					_, err := f.svc.FinishAssertion(ctx, "alice", binding, id, raw)
					return err
				case "register":
					_, err := f.svc.FinishRegistration(ctx, "alice", binding, id, raw)
					return err
				default:
					return f.svc.FinishRemoval(ctx, "alice", binding, id, raw)
				}
			}
			before, err := f.store.Load(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			for _, binding := range []string{"", "session-b"} {
				if err := finish(binding); !errors.Is(err, proof.ErrBinding) {
					t.Fatalf("wrong binding: %v", err)
				}
			}
			after, err := f.store.Load(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if after.Version != before.Version {
				t.Fatal("wrong session changed ceremony state")
			}
			if err := finish("session-a"); err != nil {
				t.Fatalf("initiating session refused: %v", err)
			}
		})
	}
}

func TestBeginRequiresCallerBinding(t *testing.T) {
	f := setup(t)
	ch := proof.Challenge([]byte(t.Name()))
	if _, _, err := f.svc.BeginAssertion(ctx, "alice", "", ch, origin, f.now.Add(time.Minute)); !errors.Is(err, proof.ErrBinding) {
		t.Fatal(err)
	}
	if _, _, err := f.svc.BeginRegistration(ctx, "alice", "", origin, f.now.Add(time.Minute)); !errors.Is(err, proof.ErrBinding) {
		t.Fatal(err)
	}
	if _, _, err := f.svc.BeginRemoval(ctx, "alice", "", nil, origin, f.now.Add(time.Minute)); !errors.Is(err, proof.ErrBinding) {
		t.Fatal(err)
	}
}

func TestChallengeReservationRollsBackWithOwnershipFailure(t *testing.T) {
	f := setup(t)
	alice, err := f.store.Load(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := f.store.Load(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	ch := proof.Challenge([]byte(t.Name()))
	bob.Pending = map[string]proof.Ceremony{"new": {Kind: "assert", Challenge: ch, ExpiresAt: f.now.Add(time.Minute)}}
	bob.User.Credentials = alice.User.Credentials // Ownership conflict after challenge reservation.
	if err = f.store.CompareAndSwap(ctx, "bob", bob.Version, bob); !errors.Is(err, proof.ErrConflict) {
		t.Fatalf("ownership failure: %v", err)
	}
	bob.User.Credentials = nil
	if err = f.store.CompareAndSwap(ctx, "bob", bob.Version, bob); err != nil {
		t.Fatalf("rolled-back reservation survived: %v", err)
	}
	// Reservation is authority-wide, including when a credential later changes owner.
	if _, _, err = f.svc.BeginAssertion(ctx, "alice", "session-a", ch, origin, f.now.Add(time.Minute)); !errors.Is(err, proof.ErrChallengeReuse) {
		t.Fatalf("cross-subject reuse: %v", err)
	}
}
