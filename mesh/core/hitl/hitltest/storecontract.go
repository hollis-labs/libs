package hitltest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh/hitl"
)

// RunStoreContract checks that a hitl.Store keeps the guarantees Service
// relies on: atomic idempotent Create, compare-and-set Swap with exactly one
// winner, immutable terminal records, the CheckSwap invariants, and
// DueForExpiry. newStore must return a fresh, empty Store on every call.
// Run it with -race; the concurrency subtests are only meaningful there.
func RunStoreContract(t *testing.T, newStore func(t *testing.T) hitl.Store) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	rec := func(id, scope, key string) hitl.Record {
		return hitl.Record{
			ItemID: id, CallerScope: scope, IdempotencyKey: key, Digest: "d-" + key, Kind: "approval",
			Request: []byte(`{"kind":"approval"}`), State: hitl.StatePresented, Revision: 1,
			CreatedAt: t0, UpdatedAt: t0,
		}
	}
	canceled := func(r hitl.Record, cause hitl.CancelCause) hitl.Record {
		n := r
		n.State, n.Revision, n.UpdatedAt = hitl.StateCanceled, r.Revision+1, t0.Add(time.Second)
		n.Outcome = hitl.Canceled{ItemID: r.ItemID, InteractionRevision: n.Revision, Cause: cause, TerminatedAt: n.UpdatedAt}
		return n
	}
	resolved := func(r hitl.Record, who string) hitl.Record {
		n := r
		n.State, n.Revision, n.UpdatedAt = hitl.StateResolved, r.Revision+1, t0.Add(time.Second)
		n.Outcome = hitl.Resolved{ItemID: r.ItemID, InteractionRevision: n.Revision, Resolution: hitl.ResolutionRecord{
			ResolutionID: "res-" + who, Response: hitl.Response{Kind: "approval", Decision: "approved"},
			Participant: hitl.Participant{PrincipalRef: who, Assurance: "asserted"}, ResolvedAt: n.UpdatedAt, InteractionRevision: n.Revision,
		}}
		return n
	}

	t.Run("CreateIsIdempotentPerScopeAndKey", func(t *testing.T) {
		s := newStore(t)
		a := rec("i1", "app-a", "k")
		got, created, err := s.Create(ctx, a)
		if err != nil || !created || got.ItemID != "i1" {
			t.Fatalf("first Create = %+v, %v, %v", got, created, err)
		}
		dup := rec("i2", "app-a", "k")
		dup.Digest = "different"
		got, created, err = s.Create(ctx, dup)
		if err != nil || created || got.ItemID != "i1" || got.Digest != "d-k" {
			t.Fatalf("duplicate Create = %+v, %v, %v; want the original, created=false", got, created, err)
		}
		other, created, err := s.Create(ctx, rec("i3", "app-b", "k"))
		if err != nil || !created || other.ItemID != "i3" {
			t.Fatalf("same key in another scope = %+v, %v, %v", other, created, err)
		}
	})

	t.Run("CreateRejectsInvalidRecord", func(t *testing.T) {
		s := newStore(t)
		bad := rec("i1", "app-a", "k")
		bad.Revision = 0
		if _, _, err := s.Create(ctx, bad); !errors.Is(err, hitl.ErrInvalidRecord) {
			t.Fatalf("Create(revision 0) = %v, want ErrInvalidRecord", err)
		}
	})

	t.Run("GetMissingIsNotFound", func(t *testing.T) {
		if _, err := newStore(t).Get(ctx, "nope"); !errors.Is(err, hitl.ErrNotFound) {
			t.Fatalf("Get = %v, want ErrNotFound", err)
		}
	})

	t.Run("GetReturnsACopy", func(t *testing.T) {
		s := newStore(t)
		if _, _, err := s.Create(ctx, rec("i1", "app-a", "k")); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Get(ctx, "i1")
		got.Request[0] = 'X'
		again, _ := s.Get(ctx, "i1")
		if !bytes.Equal(again.Request, []byte(`{"kind":"approval"}`)) {
			t.Fatalf("mutating a returned record changed the stored one: %s", again.Request)
		}
	})

	t.Run("SwapAdvancesAndPersists", func(t *testing.T) {
		s := newStore(t)
		a := rec("i1", "app-a", "k")
		if _, _, err := s.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		next := canceled(a, hitl.CauseCallerWithdrawn)
		if err := s.Swap(ctx, "i1", 1, next); err != nil {
			t.Fatalf("Swap = %v", err)
		}
		got, _ := s.Get(ctx, "i1")
		if got.State != hitl.StateCanceled || got.Revision != 2 || got.Outcome == nil {
			t.Fatalf("stored = %+v", got)
		}
	})

	t.Run("SwapWithStaleRevisionIsMismatch", func(t *testing.T) {
		s := newStore(t)
		a := rec("i1", "app-a", "k")
		if _, _, err := s.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := s.Swap(ctx, "i1", 7, canceled(a, hitl.CauseCallerWithdrawn)); !errors.Is(err, hitl.ErrRevisionMismatch) {
			t.Fatalf("Swap(stale) = %v, want ErrRevisionMismatch", err)
		}
	})

	t.Run("SwapOnMissingItemIsNotFound", func(t *testing.T) {
		s := newStore(t)
		if err := s.Swap(ctx, "nope", 1, canceled(rec("nope", "a", "k"), hitl.CauseCallerWithdrawn)); !errors.Is(err, hitl.ErrNotFound) {
			t.Fatalf("Swap = %v, want ErrNotFound", err)
		}
	})

	t.Run("TerminalRecordNeverChanges", func(t *testing.T) {
		s := newStore(t)
		a := rec("i1", "app-a", "k")
		if _, _, err := s.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		first := resolved(a, "alice")
		if err := s.Swap(ctx, "i1", 1, first); err != nil {
			t.Fatal(err)
		}
		// Even a swap that names the right revision and a legal-looking record
		// must not touch a terminal record.
		second := canceled(first, hitl.CauseCallerWithdrawn)
		if err := s.Swap(ctx, "i1", 2, second); !errors.Is(err, hitl.ErrTerminalRecord) {
			t.Fatalf("Swap(terminal) = %v, want ErrTerminalRecord", err)
		}
		got, _ := s.Get(ctx, "i1")
		if got.State != hitl.StateResolved || got.Revision != 2 {
			t.Fatalf("terminal record changed: %+v", got)
		}
	})

	t.Run("SwapEnforcesRecordInvariants", func(t *testing.T) {
		a := rec("i1", "app-a", "k")
		cases := map[string]func(n *hitl.Record){
			"illegal transition": func(n *hitl.Record) { n.State = hitl.StateStaged; n.Outcome = nil },
			"revision skipped": func(n *hitl.Record) {
				n.Revision = 5
				n.Outcome = hitl.Canceled{ItemID: "i1", InteractionRevision: 5, Cause: hitl.CauseCallerWithdrawn, TerminatedAt: t0}
			},
			"terminal without outcome":  func(n *hitl.Record) { n.Outcome = nil },
			"immutable request changed": func(n *hitl.Record) { n.Request = []byte(`{"kind":"other"}`) },
			"immutable key changed":     func(n *hitl.Record) { n.IdempotencyKey = "other" },
			"immutable scope changed":   func(n *hitl.Record) { n.CallerScope = "app-z" },
			"outcome for another item": func(n *hitl.Record) {
				n.Outcome = hitl.Canceled{ItemID: "zzz", InteractionRevision: 2, Cause: hitl.CauseCallerWithdrawn, TerminatedAt: t0}
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				s := newStore(t)
				if _, _, err := s.Create(ctx, a); err != nil {
					t.Fatal(err)
				}
				n := canceled(a, hitl.CauseCallerWithdrawn)
				mutate(&n)
				if err := s.Swap(ctx, "i1", 1, n); !errors.Is(err, hitl.ErrInvalidRecord) {
					t.Fatalf("Swap = %v, want ErrInvalidRecord", err)
				}
				got, _ := s.Get(ctx, "i1")
				if got.Revision != 1 || got.State != hitl.StatePresented {
					t.Fatalf("rejected swap changed the record: %+v", got)
				}
			})
		}
	})

	t.Run("ConcurrentCreateHasOneWinner", func(t *testing.T) {
		s := newStore(t)
		const n = 32
		var wg sync.WaitGroup
		created := make(chan string, n)
		ids := make(chan string, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, ok, err := s.Create(ctx, rec(fmt.Sprintf("item-%d", i), "app-a", "same-key"))
				if err != nil {
					t.Error(err)
					return
				}
				ids <- got.ItemID
				if ok {
					created <- got.ItemID
				}
			}()
		}
		wg.Wait()
		close(created)
		close(ids)
		if len(created) != 1 {
			t.Fatalf("%d Creates reported created=true, want exactly 1", len(created))
		}
		winner := <-created
		for id := range ids {
			if id != winner {
				t.Fatalf("a Create returned item %q, want the single winner %q", id, winner)
			}
		}
	})

	t.Run("ConcurrentSwapHasOneWinnerAndItsOutcomeSticks", func(t *testing.T) {
		for round := range 20 {
			s := newStore(t)
			a := rec("i1", "app-a", fmt.Sprintf("k%d", round))
			if _, _, err := s.Create(ctx, a); err != nil {
				t.Fatal(err)
			}
			const n = 16
			var wg sync.WaitGroup
			results := make([]error, n)
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					next := resolved(a, fmt.Sprintf("p%d", i))
					if i%2 == 1 {
						next = canceled(a, hitl.CauseCallerWithdrawn)
					}
					results[i] = s.Swap(ctx, "i1", 1, next)
				}()
			}
			wg.Wait()
			winners := 0
			for _, err := range results {
				switch {
				case err == nil:
					winners++
				case errors.Is(err, hitl.ErrRevisionMismatch), errors.Is(err, hitl.ErrTerminalRecord):
				default:
					t.Fatalf("loser got %v, want ErrRevisionMismatch or ErrTerminalRecord", err)
				}
			}
			if winners != 1 {
				t.Fatalf("round %d: %d Swaps succeeded, want exactly 1", round, winners)
			}
			got, _ := s.Get(ctx, "i1")
			if got.Revision != 2 || got.Outcome == nil {
				t.Fatalf("stored = %+v", got)
			}
		}
	})

	t.Run("DueForExpiry", func(t *testing.T) {
		s := newStore(t)
		at := func(d time.Duration) *time.Time { x := t0.Add(d); return &x }
		mk := func(id string, exp *time.Time) hitl.Record {
			r := rec(id, "app-a", id)
			r.ExpiresAt = exp
			return r
		}
		for _, r := range []hitl.Record{
			mk("late", at(2*time.Hour)), mk("none", nil), mk("due-b", at(time.Minute)),
			mk("due-a", at(-time.Minute)), mk("exact", at(0)), mk("done", at(-time.Hour)),
		} {
			if _, _, err := s.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		done, _ := s.Get(ctx, "done")
		if err := s.Swap(ctx, "done", 1, canceled(done, hitl.CauseCallerWithdrawn)); err != nil {
			t.Fatal(err)
		}
		got, err := s.DueForExpiry(ctx, t0.Add(time.Minute), 10)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range got {
			ids = append(ids, r.ItemID)
		}
		want := []string{"due-a", "exact", "due-b"}
		if fmt.Sprint(ids) != fmt.Sprint(want) {
			t.Fatalf("DueForExpiry = %v, want %v (earliest first; terminal, unexpiring and future items excluded; the boundary instant is due)", ids, want)
		}
		limited, _ := s.DueForExpiry(ctx, t0.Add(time.Minute), 2)
		if len(limited) != 2 {
			t.Fatalf("limit 2 returned %d", len(limited))
		}
	})
}
