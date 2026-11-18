package chattest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
)

// ControlOption configures [ControlConformance].
type ControlOption func(*controlSuite)

// WithBump tells the suite how to raise a conversation's generation on a
// Control that does not implement chat.Bumper because its host bumps in its
// own transaction. Without it, and without a Bumper, the generation cases are
// skipped.
func WithBump(bump func(t *testing.T, c chat.Control, conversationID string)) ControlOption {
	return func(s *controlSuite) { s.bump = bump }
}

type controlSuite struct {
	newControl func(t *testing.T) chat.Control
	bump       func(t *testing.T, c chat.Control, conversationID string)
	ids        func(string) string
}

const ttl = 90 * time.Second

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func (s *controlSuite) conv(t *testing.T, c chat.Control, agent string) chat.Conversation {
	t.Helper()
	conv, created, err := c.Ensure(context.Background(), chat.NewConversation{
		ID: s.ids("conv"), Agent: agent, Scope: "seat-1", Owner: "owner-1", Kind: "stable", At: t0,
	})
	if err != nil || !created {
		t.Fatalf("Ensure = created %v, %v", created, err)
	}
	return conv
}

func (s *controlSuite) attach(t *testing.T, c chat.Control, conversationID string, role chat.Role) chat.Attachment {
	t.Helper()
	a, err := c.Attach(context.Background(), chat.Attachment{
		ID: s.ids("att"), ConversationID: conversationID, Role: role, Stream: chat.StreamDeltas, CreatedAt: t0,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return a
}

func wantErr(t *testing.T, what string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: err = %v, want %v", what, err, target)
	}
}

func mustGet(t *testing.T, c chat.Control, id string) chat.Conversation {
	t.Helper()
	conv, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return conv
}

// ControlConformance checks a Control against the lease rules of the wire.
// newControl returns a fresh, empty Control for each case.
func ControlConformance(t *testing.T, newControl func(t *testing.T) chat.Control, opts ...ControlOption) {
	t.Helper()
	s := &controlSuite{newControl: newControl, ids: IDs()}
	for _, o := range opts {
		o(s)
	}
	ctx := context.Background()

	t.Run("EnsureIsOncePerAgent", func(t *testing.T) {
		c := s.newControl(t)
		first := s.conv(t, c, "agent-a")
		if first.Generation != 1 || first.Agent != "agent-a" || first.Owner != "owner-1" || first.Scope != "seat-1" || first.Kind != "stable" {
			t.Fatalf("new conversation = %+v", first)
		}
		again, created, err := c.Ensure(ctx, chat.NewConversation{ID: s.ids("conv"), Agent: "agent-a", At: t0})
		if err != nil || created || again.ID != first.ID {
			t.Fatalf("second Ensure = %+v, created %v, %v; want the first conversation", again, created, err)
		}
		other := s.conv(t, c, "agent-b")
		if other.ID == first.ID {
			t.Fatal("two agents share a conversation")
		}
		_, err = c.Get(ctx, "no-such-conversation")
		wantErr(t, "Get unknown", err, chat.ErrNotFound)
		if err := c.SetRunnerRef(ctx, first.ID, "ref-1"); err != nil {
			t.Fatalf("SetRunnerRef: %v", err)
		}
		if got := mustGet(t, c, first.ID); got.RunnerRef != "ref-1" {
			t.Fatalf("RunnerRef = %q", got.RunnerRef)
		}
	})

	t.Run("Attachments", func(t *testing.T) {
		c := s.newControl(t)
		conv := s.conv(t, c, "agent-a")
		a := s.attach(t, c, conv.ID, chat.RoleViewer)
		if a.Generation != 1 {
			t.Fatalf("attachment generation = %d, want 1", a.Generation)
		}
		got, err := c.Attachment(ctx, conv.ID, a.ID)
		if err != nil || got.Role != chat.RoleViewer || got.Stream != chat.StreamDeltas || got.ID != a.ID {
			t.Fatalf("Attachment = %+v, %v", got, err)
		}
		_, err = c.Attachment(ctx, conv.ID, "no-such-attachment")
		wantErr(t, "unknown attachment", err, chat.ErrUnknownAttachment)
		if released, derr := c.Detach(ctx, conv.ID, a.ID, t0); derr != nil || released {
			t.Fatalf("Detach = released %v, %v", released, derr)
		}
		_, err = c.Detach(ctx, conv.ID, a.ID, t0)
		wantErr(t, "second Detach", err, chat.ErrUnknownAttachment)
	})

	t.Run("OneLeaseAndOnlyForAController", func(t *testing.T) {
		c := s.newControl(t)
		conv := s.conv(t, c, "agent-a")
		a := s.attach(t, c, conv.ID, chat.RoleController)
		b := s.attach(t, c, conv.ID, chat.RoleController)
		v := s.attach(t, c, conv.ID, chat.RoleViewer)

		_, err := c.Acquire(ctx, conv.ID, v.ID, t0, ttl)
		wantErr(t, "viewer Acquire", err, chat.ErrNotController)
		_, err = c.Takeover(ctx, conv.ID, v.ID, t0, ttl)
		wantErr(t, "viewer Takeover", err, chat.ErrNotController)

		ch, err := c.Acquire(ctx, conv.ID, a.ID, t0, ttl)
		if err != nil || !ch.Acquired || ch.Lease.Holder != a.ID || !ch.Lease.ExpiresAt.Equal(t0.Add(ttl)) || ch.Previous != "" {
			t.Fatalf("first Acquire = %+v, %v", ch, err)
		}
		_, err = c.Acquire(ctx, conv.ID, b.ID, t0.Add(time.Second), ttl)
		var held *chat.LeaseHeldError
		if !errors.As(err, &held) || held.Lease.Holder != a.ID {
			t.Fatalf("contending Acquire err = %v, want a LeaseHeldError naming the holder", err)
		}
		if err := c.Holds(ctx, conv.ID, a.ID, t0.Add(time.Second)); err != nil {
			t.Fatalf("holder Holds: %v", err)
		}
		wantErr(t, "other Holds", c.Holds(ctx, conv.ID, b.ID, t0.Add(time.Second)), chat.ErrLeaseRequired)
		if l, ok := mustGet(t, c, conv.ID).LeaseAt(t0.Add(time.Second)); !ok || l.Holder != a.ID {
			t.Fatalf("stored lease = %+v, %v", l, ok)
		}
	})

	t.Run("RenewAndExpiry", func(t *testing.T) {
		c := s.newControl(t)
		conv := s.conv(t, c, "agent-a")
		a := s.attach(t, c, conv.ID, chat.RoleController)
		b := s.attach(t, c, conv.ID, chat.RoleController)
		if _, err := c.Acquire(ctx, conv.ID, a.ID, t0, ttl); err != nil {
			t.Fatal(err)
		}
		at := t0.Add(60 * time.Second)
		ch, err := c.Acquire(ctx, conv.ID, a.ID, at, ttl)
		if err != nil || ch.Acquired || !ch.Lease.ExpiresAt.Equal(at.Add(ttl)) {
			t.Fatalf("renewal = %+v, %v; want Acquired false and a later expiry", ch, err)
		}
		// Not renewed again: the lease lapses at its expiry, exactly.
		lapse := at.Add(ttl)
		if herr := c.Holds(ctx, conv.ID, a.ID, lapse.Add(-time.Nanosecond)); herr != nil {
			t.Fatalf("Holds just before expiry: %v", herr)
		}
		wantErr(t, "Holds at expiry", c.Holds(ctx, conv.ID, a.ID, lapse), chat.ErrLeaseRequired)
		ch, err = c.Acquire(ctx, conv.ID, b.ID, lapse, ttl)
		if err != nil || !ch.Acquired || ch.Lease.Holder != b.ID || ch.Previous != "" {
			t.Fatalf("Acquire after expiry = %+v, %v", ch, err)
		}
		_, err = c.BeginRun(ctx, conv.ID, "run-1", a.ID, lapse, ttl)
		wantErr(t, "expired holder's submit", err, chat.ErrLeaseRequired)
	})

	t.Run("TakeoverIsAtomic", func(t *testing.T) {
		c := s.newControl(t)
		conv := s.conv(t, c, "agent-a")
		a := s.attach(t, c, conv.ID, chat.RoleController)
		b := s.attach(t, c, conv.ID, chat.RoleController)
		if _, err := c.Acquire(ctx, conv.ID, a.ID, t0, ttl); err != nil {
			t.Fatal(err)
		}
		ch, err := c.Takeover(ctx, conv.ID, b.ID, t0.Add(time.Second), ttl)
		if err != nil || !ch.Acquired || ch.Lease.Holder != b.ID || ch.Previous != a.ID {
			t.Fatalf("Takeover = %+v, %v", ch, err)
		}
		_, err = c.BeginRun(ctx, conv.ID, "run-1", a.ID, t0.Add(2*time.Second), ttl)
		wantErr(t, "previous holder's submit", err, chat.ErrLeaseRequired)
		if got := mustGet(t, c, conv.ID); got.ActiveRun != "" {
			t.Fatalf("a refused submit left active run %q", got.ActiveRun)
		}
		if _, err := c.BeginRun(ctx, conv.ID, "run-2", b.ID, t0.Add(2*time.Second), ttl); err != nil {
			t.Fatalf("new holder's submit: %v", err)
		}
		// Taking over one's own lease renews it.
		if ch, err := c.Takeover(ctx, conv.ID, b.ID, t0.Add(3*time.Second), ttl); err != nil || ch.Acquired {
			t.Fatalf("Takeover by the holder = %+v, %v", ch, err)
		}
	})

	t.Run("SubmitAcquiresAFreeLease", func(t *testing.T) {
		c := s.newControl(t)
		conv := s.conv(t, c, "agent-a")
		a := s.attach(t, c, conv.ID, chat.RoleController)
		b := s.attach(t, c, conv.ID, chat.RoleController)
		v := s.attach(t, c, conv.ID, chat.RoleViewer)

		_, err := c.BeginRun(ctx, conv.ID, "run-0", v.ID, t0, ttl)
		wantErr(t, "viewer submit", err, chat.ErrLeaseRequired)
		_, err = c.BeginRun(ctx, conv.ID, "run-0", "no-such-attachment", t0, ttl)
		wantErr(t, "unknown attachment submit", err, chat.ErrUnknownAttachment)

		ch, err := c.BeginRun(ctx, conv.ID, "run-1", a.ID, t0, ttl)
		if err != nil || !ch.Acquired || ch.Lease.Holder != a.ID {
			t.Fatalf("submit with a free lease = %+v, %v", ch, err)
		}
		if got := mustGet(t, c, conv.ID); got.ActiveRun != "run-1" {
			t.Fatalf("active run = %q", got.ActiveRun)
		}
		_, err = c.BeginRun(ctx, conv.ID, "run-2", a.ID, t0, ttl)
		wantErr(t, "submit during a run", err, chat.ErrBusy)
		_, err = c.BeginRun(ctx, conv.ID, "run-2", b.ID, t0, ttl)
		wantErr(t, "non-holder submit", err, chat.ErrLeaseRequired)
		_, err = c.BeginRun(ctx, conv.ID, "run-2", "", t0, ttl)
		wantErr(t, "host submit during a run", err, chat.ErrBusy)

		// A refused submit must not acquire: the lease has lapsed, the run has not.
		late := t0.Add(2 * ttl)
		_, err = c.BeginRun(ctx, conv.ID, "run-2", b.ID, late, ttl)
		wantErr(t, "submit during a run, lease free", err, chat.ErrBusy)
		if _, ok := mustGet(t, c, conv.ID).LeaseAt(late); ok {
			t.Fatal("a submit refused as busy took the lease")
		}

		if err := c.EndRun(ctx, conv.ID, "some-other-run"); err != nil {
			t.Fatalf("EndRun of another run: %v", err)
		}
		if got := mustGet(t, c, conv.ID); got.ActiveRun != "run-1" {
			t.Fatalf("EndRun of another run cleared the active run (%q)", got.ActiveRun)
		}
		if err := c.EndRun(ctx, conv.ID, "run-1"); err != nil {
			t.Fatalf("EndRun: %v", err)
		}
		if err := c.EndRun(ctx, conv.ID, "run-1"); err != nil {
			t.Fatalf("EndRun twice: %v", err)
		}
		// The holder submits without the lease changing hands.
		if _, err := c.Acquire(ctx, conv.ID, b.ID, late, ttl); err != nil {
			t.Fatal(err)
		}
		if ch, err := c.BeginRun(ctx, conv.ID, "run-3", b.ID, late, ttl); err != nil || ch.Acquired {
			t.Fatalf("holder submit = %+v, %v", ch, err)
		}
		if err := c.EndRun(ctx, conv.ID, "run-3"); err != nil {
			t.Fatal(err)
		}
		// A run the host starts needs no lease, whoever holds it.
		if ch, err := c.BeginRun(ctx, conv.ID, "run-4", "", late, ttl); err != nil || ch.Acquired {
			t.Fatalf("host submit = %+v, %v", ch, err)
		}
	})

	t.Run("DetachAndExpiryReleaseTheLeaseNotTheRun", func(t *testing.T) {
		c := s.newControl(t)
		conv := s.conv(t, c, "agent-a")
		a := s.attach(t, c, conv.ID, chat.RoleController)
		b := s.attach(t, c, conv.ID, chat.RoleController)
		if _, err := c.BeginRun(ctx, conv.ID, "run-1", a.ID, t0, ttl); err != nil {
			t.Fatal(err)
		}
		if released, err := c.Detach(ctx, conv.ID, b.ID, t0); err != nil || released {
			t.Fatalf("Detach of a non-holder = released %v, %v", released, err)
		}
		released, err := c.Detach(ctx, conv.ID, a.ID, t0.Add(time.Second))
		if err != nil || !released {
			t.Fatalf("Detach of the holder = released %v, %v", released, err)
		}
		got := mustGet(t, c, conv.ID)
		if _, ok := got.LeaseAt(t0.Add(time.Second)); ok || got.ActiveRun != "run-1" {
			t.Fatalf("after detach: lease held %v, active run %q; want no lease and the run still active", ok, got.ActiveRun)
		}
		b2 := s.attach(t, c, conv.ID, chat.RoleController)
		if ch, err := c.Acquire(ctx, conv.ID, b2.ID, t0.Add(2*time.Second), ttl); err != nil || !ch.Acquired {
			t.Fatalf("Acquire after detach = %+v, %v", ch, err)
		}
		if got := mustGet(t, c, conv.ID); got.ActiveRun != "run-1" {
			t.Fatalf("active run after lease change = %q", got.ActiveRun)
		}
	})

	t.Run("ConcurrentAcquireHasOneWinner", func(t *testing.T) {
		c := s.newControl(t)
		conv := s.conv(t, c, "agent-a")
		const n = 8
		atts := make([]chat.Attachment, n)
		for i := range atts {
			atts[i] = s.attach(t, c, conv.ID, chat.RoleController)
		}
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			wins int
		)
		for _, a := range atts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ch, err := c.Acquire(ctx, conv.ID, a.ID, t0, ttl)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil && ch.Acquired:
					wins++
				case !errors.Is(err, chat.ErrLeaseHeld):
					t.Errorf("Acquire: %+v, %v", ch, err)
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("%d attachments acquired the lease, want 1", wins)
		}
	})

	t.Run("GenerationInvalidatesAttachmentsAndLease", func(t *testing.T) {
		c := s.newControl(t)
		bump := s.bump
		if b, ok := c.(chat.Bumper); ok && bump == nil {
			bump = func(t *testing.T, _ chat.Control, id string) {
				t.Helper()
				got, err := b.Bump(ctx, id)
				if err != nil || got.Generation < 2 {
					t.Fatalf("Bump = %+v, %v", got, err)
				}
			}
		}
		if bump == nil {
			t.Skip("the Control is not a Bumper and no WithBump was given")
		}
		conv := s.conv(t, c, "agent-a")
		a := s.attach(t, c, conv.ID, chat.RoleController)
		if _, err := c.BeginRun(ctx, conv.ID, "run-1", a.ID, t0, ttl); err != nil {
			t.Fatal(err)
		}
		bump(t, c, conv.ID)
		got := mustGet(t, c, conv.ID)
		if got.Generation != 2 || got.ActiveRun != "" || got.Lease != nil || got.ID != conv.ID {
			t.Fatalf("after a bump = %+v; want generation 2, no run, no lease, the same ID", got)
		}
		stale := func(what string, err error) {
			t.Helper()
			var se *chat.StaleError
			if !errors.As(err, &se) || se.Generation != 2 {
				t.Fatalf("%s with an old attachment: err = %v, want a StaleError carrying generation 2", what, err)
			}
		}
		_, err := c.Attachment(ctx, conv.ID, a.ID)
		stale("Attachment", err)
		_, err = c.Acquire(ctx, conv.ID, a.ID, t0, ttl)
		stale("Acquire", err)
		_, err = c.Takeover(ctx, conv.ID, a.ID, t0, ttl)
		stale("Takeover", err)
		stale("Holds", c.Holds(ctx, conv.ID, a.ID, t0))
		_, err = c.BeginRun(ctx, conv.ID, "run-2", a.ID, t0, ttl)
		stale("BeginRun", err)
		_, err = c.Detach(ctx, conv.ID, a.ID, t0)
		stale("Detach", err)

		// Whoever re-acquires first holds the lease.
		fresh := s.attach(t, c, conv.ID, chat.RoleController)
		if fresh.Generation != 2 {
			t.Fatalf("new attachment generation = %d, want 2", fresh.Generation)
		}
		if ch, err := c.Acquire(ctx, conv.ID, fresh.ID, t0, ttl); err != nil || !ch.Acquired {
			t.Fatalf("Acquire after a bump = %+v, %v", ch, err)
		}
	})

	t.Run("Reset", func(t *testing.T) {
		c := s.newControl(t)
		conv := s.conv(t, c, "agent-a")
		a := s.attach(t, c, conv.ID, chat.RoleController)
		b := s.attach(t, c, conv.ID, chat.RoleController)
		next := func() chat.NewConversation {
			return chat.NewConversation{ID: s.ids("conv"), Agent: "agent-a", Scope: "seat-1", Owner: "owner-1", Kind: "stable", At: t0}
		}
		if _, err := c.BeginRun(ctx, conv.ID, "run-1", a.ID, t0, ttl); err != nil {
			t.Fatal(err)
		}
		_, err := c.Reset(ctx, conv.ID, a.ID, next(), t0)
		wantErr(t, "Reset during a run", err, chat.ErrBusy)
		if err = c.EndRun(ctx, conv.ID, "run-1"); err != nil {
			t.Fatal(err)
		}
		_, err = c.Reset(ctx, conv.ID, b.ID, next(), t0)
		wantErr(t, "Reset by a non-holder", err, chat.ErrLeaseRequired)
		_, err = c.Reset(ctx, conv.ID, "", next(), t0)
		wantErr(t, "Reset by nobody while the lease is held", err, chat.ErrLeaseRequired)

		n := next()
		fresh, err := c.Reset(ctx, conv.ID, a.ID, n, t0)
		if err != nil || fresh.ID != n.ID || fresh.Generation != 1 || fresh.Agent != "agent-a" || fresh.Closed {
			t.Fatalf("Reset = %+v, %v", fresh, err)
		}
		old := mustGet(t, c, conv.ID)
		if !old.Closed || old.Successor != fresh.ID {
			t.Fatalf("old conversation = %+v; want closed with its successor", old)
		}
		closed := func(what string, err error) {
			t.Helper()
			var ce *chat.ClosedError
			if !errors.As(err, &ce) || ce.Successor != fresh.ID {
				t.Fatalf("%s on a closed conversation: err = %v, want a ClosedError naming %s", what, err, fresh.ID)
			}
		}
		_, err = c.Attach(ctx, chat.Attachment{ID: s.ids("att"), ConversationID: conv.ID, Role: chat.RoleViewer, Stream: chat.StreamDeltas, CreatedAt: t0})
		closed("Attach", err)
		_, err = c.Attachment(ctx, conv.ID, a.ID)
		closed("Attachment", err)
		_, err = c.Acquire(ctx, conv.ID, a.ID, t0, ttl)
		closed("Acquire", err)
		closed("Holds", c.Holds(ctx, conv.ID, a.ID, t0))
		_, err = c.BeginRun(ctx, conv.ID, "run-2", "", t0, ttl)
		closed("BeginRun", err)
		_, err = c.Reset(ctx, conv.ID, "", next(), t0)
		closed("Reset", err)

		cur, created, err := c.Ensure(ctx, next())
		if err != nil || created || cur.ID != fresh.ID {
			t.Fatalf("Ensure after a reset = %+v, created %v, %v; want the successor", cur, created, err)
		}
		// With nobody holding the lease, anyone may reset.
		if _, err := c.Reset(ctx, fresh.ID, "", next(), t0); err != nil {
			t.Fatalf("Reset with a free lease: %v", err)
		}
	})
}
