package mw_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/util/mw"
)

type H = mw.Handler[string, string]
type M = mw.Member[string, string]

func tag(name string, phase mw.Phase, fm mw.FailureMode, trace *[]string) M {
	return M{
		Desc: mw.Descriptor{Name: name, Phase: phase, Failure: fm},
		MW: func(next H) H {
			return func(ctx context.Context, r string) (string, error) {
				*trace = append(*trace, name+">")
				out, err := next(ctx, r)
				*trace = append(*trace, "<"+name)
				return out, err
			}
		},
	}
}

func TestChainOrderFirstIsOutermost(t *testing.T) {
	var tr []string
	s, err := mw.Assemble(mw.Options{},
		tag("a", mw.PhaseRecover, mw.Closed, &tr),
		tag("b", mw.PhaseTrace, mw.Open, &tr),
	)
	if err != nil {
		t.Fatal(err)
	}
	s.Wrap(func(context.Context, string) (string, error) { tr = append(tr, "h"); return "ok", nil })(context.Background(), "x")
	if got := strings.Join(tr, " "); got != "a> b> h <b <a" {
		t.Fatalf("order: %s", got)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string][]mw.Descriptor{
		"phase order":      {{Name: "a", Phase: mw.PhaseAuthn, Failure: mw.Closed}, {Name: "b", Phase: mw.PhaseTrace, Failure: mw.Open}},
		"unset failure":    {{Name: "a", Phase: mw.PhaseTrace}},
		"observer closed":  {{Name: "a", Phase: mw.PhaseAccessLog, Failure: mw.Closed}},
		"protector open":   {{Name: "a", Phase: mw.PhaseAuthz, Failure: mw.Open}},
		"duplicate":        {{Name: "a", Phase: mw.PhaseTrace, Failure: mw.Open}, {Name: "a", Phase: mw.PhaseTrace, Failure: mw.Open}},
		"mutates unlisted": {{Name: "a", Phase: mw.PhaseIdentity, Failure: mw.Open, Mutates: true}},
		"missing requires": {{Name: "a", Phase: mw.PhaseAuthz, Failure: mw.Closed, Requires: []string{"principal"}}},
		"requires inner":   {{Name: "a", Phase: mw.PhaseAuthz, Failure: mw.Closed, Requires: []string{"principal"}}, {Name: "b", Phase: mw.PhaseIdentity, Failure: mw.Open, Provides: []string{"principal"}}},
	}
	for name, d := range cases {
		if err := mw.Validate(d, mw.Options{}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	ok := []mw.Descriptor{
		{Name: "authn", Phase: mw.PhaseAuthn, Failure: mw.Closed, Provides: []string{"principal"}},
		{Name: "authz", Phase: mw.PhaseAuthz, Failure: mw.Closed, Requires: []string{"principal"}},
		{Name: "log", Phase: mw.PhaseAccessLog, Failure: mw.Closed, Reason: "audit log is the record"},
	}
	// access-log after authz violates phase order; reorder:
	ok = []mw.Descriptor{ok[2], ok[0], ok[1]}
	if err := mw.Validate(ok, mw.Options{}); err != nil {
		t.Fatal(err)
	}
}

func faulty(name string, phase mw.Phase, fm mw.FailureMode, afterNext bool, how string) M {
	return M{
		Desc: mw.Descriptor{Name: name, Phase: phase, Failure: fm, Reason: "test"},
		MW: func(next H) H {
			return func(ctx context.Context, r string) (string, error) {
				broke := func() (string, error) {
					if how == "panic" {
						panic("boom")
					}
					return "", mw.NewFault(errors.New("redis down"))
				}
				if !afterNext {
					return broke()
				}
				out, err := next(ctx, r)
				if _, e := broke(); e != nil {
					return "", e
				}
				return out, err
			}
		},
	}
}

func TestFailureModes(t *testing.T) {
	for _, how := range []string{"panic", "fault"} {
		calls := 0
		h := func(context.Context, string) (string, error) { calls++; return "ok", nil }

		s, _ := mw.Assemble(mw.Options{}, faulty("m", mw.PhaseTrace, mw.Open, false, how))
		if out, err := s.Wrap(h)(context.Background(), "x"); err != nil || out != "ok" || calls != 1 {
			t.Fatalf("%s open before next: out=%q err=%v calls=%d", how, out, err, calls)
		}
		calls = 0
		s, _ = mw.Assemble(mw.Options{}, faulty("m", mw.PhaseTrace, mw.Open, true, how))
		if out, err := s.Wrap(h)(context.Background(), "x"); err != nil || out != "ok" || calls != 1 {
			t.Fatalf("%s open after next must not rerun handler: out=%q err=%v calls=%d", how, out, err, calls)
		}
		calls = 0
		s, _ = mw.Assemble(mw.Options{}, faulty("m", mw.PhaseAuthz, mw.Closed, false, how))
		if _, err := s.Wrap(h)(context.Background(), "x"); mw.AsFault(err) == nil || calls != 0 {
			t.Fatalf("%s closed: err=%v calls=%d", how, err, calls)
		}
	}
}

func TestDownstreamPanicIsNotBlamedOnOpenMember(t *testing.T) {
	var events []mw.Event
	s, _ := mw.Assemble(mw.Options{Observer: func(e mw.Event) { events = append(events, e) }},
		mw.RecoverMember[string, string](),
		tag("t", mw.PhaseTrace, mw.Open, new([]string)),
	)
	_, err := s.Wrap(func(context.Context, string) (string, error) { panic("handler bug") })(context.Background(), "x")
	if !errors.Is(err, mw.ErrPanic) {
		t.Fatalf("want ErrPanic from outer recover, got %v", err)
	}
	for _, e := range events {
		if e.Outcome == mw.Faulted {
			t.Fatalf("handler panic was blamed on %s", e.Name)
		}
	}
}

func deny(name string, phase mw.Phase) M {
	return M{
		Desc: mw.Descriptor{Name: name, Phase: phase, Failure: mw.Closed},
		MW: func(next H) H {
			return func(ctx context.Context, r string) (string, error) {
				if r == "bad" {
					return "", errors.New("denied by " + name)
				}
				return next(ctx, r)
			}
		},
	}
}

func TestShadowRecordsButDoesNotApply(t *testing.T) {
	var events []mw.Event
	obs := func(e mw.Event) { events = append(events, e) }

	m := deny("authz", mw.PhaseAuthz)
	m.Mode = mw.Shadow
	s, _ := mw.Assemble(mw.Options{Observer: obs}, m)
	out, err := s.Wrap(func(_ context.Context, r string) (string, error) { return "served " + r, nil })(context.Background(), "bad")
	if err != nil || out != "served bad" {
		t.Fatalf("shadow must not block: %q %v", out, err)
	}
	if len(events) != 1 || events[0].Outcome != mw.Denied || events[0].Mode != mw.Shadow {
		t.Fatalf("events: %+v", events)
	}

	m.Mode = mw.Enforce
	events = nil
	s, _ = mw.Assemble(mw.Options{Observer: obs}, m)
	if _, err := s.Wrap(func(_ context.Context, r string) (string, error) { return r, nil })(context.Background(), "bad"); err == nil {
		t.Fatal("enforce must block")
	}
	if events[0].Outcome != mw.Denied {
		t.Fatalf("events: %+v", events)
	}
}

func TestOffSkips(t *testing.T) {
	m := deny("authz", mw.PhaseAuthz)
	m.Mode = mw.Off
	s, _ := mw.Assemble(mw.Options{}, m)
	if _, err := s.Wrap(func(_ context.Context, r string) (string, error) { return r, nil })(context.Background(), "bad"); err != nil {
		t.Fatal(err)
	}
}

func TestExplain(t *testing.T) {
	var tr []string
	s, _ := mw.Assemble(mw.Options{}, mw.RecoverMember[string, string](), tag("t", mw.PhaseTrace, mw.Open, &tr))
	out := s.Explain()
	if !strings.Contains(out, "recover") || !strings.Contains(out, "closed") || !strings.Contains(out, "open") {
		t.Fatal(out)
	}
}
