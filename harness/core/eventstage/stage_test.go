package eventstage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ev is a minimal envelope for exercising the chain.
type ev struct {
	Payload   string
	Sanitized bool
	Trail     []string
}

type fakeSanitize struct {
	fn     func(e *ev) error
	closed []error
}

func (f *fakeSanitize) Name() string { return "fake-sanitize" }
func (f *fakeSanitize) Apply(_ context.Context, e *ev) (Outcome, error) {
	e.Trail = append(e.Trail, "sanitize")
	if err := f.fn(e); err != nil {
		return Outcome{}, err
	}
	e.Sanitized = true
	return Outcome{Changed: true}, nil
}
func (f *fakeSanitize) FailClosed(e *ev, cause error) {
	f.closed = append(f.closed, cause)
	e.Payload, e.Sanitized = `{"sanitize_error":"x"}`, false
}

func scrub(e *ev) error {
	e.Payload = strings.ReplaceAll(e.Payload, "SECRET", "[redacted]")
	return nil
}

func trail(name string, out Outcome, err error) Stage[ev] {
	return Named(name, func(_ context.Context, e *ev) (Outcome, error) {
		e.Trail = append(e.Trail, name)
		return out, err
	})
}

func TestNewChainRequiresSanitize(t *testing.T) {
	if _, err := NewChain(Config[ev]{Stamp: trail("stamp", Outcome{}, nil)}); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestChainRunsStampSanitizeClassifyInOrder(t *testing.T) {
	c, _ := NewChain(Config[ev]{
		// Registered out of order on purpose: the slot decides, not the call site.
		Classify: trail("classify", Outcome{}, nil),
		Sanitize: &fakeSanitize{fn: scrub},
		Stamp:    trail("stamp", Outcome{}, nil),
	})
	e := &ev{Payload: "x"}
	rep := c.Apply(t.Context(), e)
	if got := strings.Join(e.Trail, ","); got != "stamp,sanitize,classify" {
		t.Fatalf("order = %s", got)
	}
	if !rep.Emit() || rep.Err() != nil {
		t.Fatalf("report = %+v err=%v", rep, rep.Err())
	}
}

// Classify must observe the sanitized payload, never the original.
func TestClassifyNeverSeesTheUnsanitizedPayload(t *testing.T) {
	var seen string
	classify := Named("classify", func(_ context.Context, e *ev) (Outcome, error) {
		seen = e.Payload
		return Outcome{}, nil
	})
	c, _ := NewChain(Config[ev]{Sanitize: &fakeSanitize{fn: scrub}, Classify: classify})
	c.Apply(t.Context(), &ev{Payload: "token=SECRET"})
	if strings.Contains(seen, "SECRET") || seen == "" {
		t.Fatalf("classify saw %q", seen)
	}
}

func TestSanitizeErrorFailsClosedAndClassifyStillRunsOnTheScrubbedEvent(t *testing.T) {
	fs := &fakeSanitize{fn: func(*ev) error { return errors.New("boom") }}
	var seen string
	classify := Named("classify", func(_ context.Context, e *ev) (Outcome, error) { seen = e.Payload; return Outcome{}, nil })
	c, _ := NewChain(Config[ev]{Sanitize: fs, Classify: classify})
	e := &ev{Payload: "token=SECRET"}
	rep := c.Apply(t.Context(), e)
	if len(fs.closed) != 1 || e.Sanitized || strings.Contains(e.Payload, "SECRET") {
		t.Fatalf("closed=%v sanitized=%v payload=%q", fs.closed, e.Sanitized, e.Payload)
	}
	if strings.Contains(seen, "SECRET") || seen == "" {
		t.Fatalf("classify saw %q after fail-closed", seen)
	}
	if !rep.Emit() || !rep.Steps[0].FailedClosed || rep.Err() == nil {
		t.Fatalf("report = %+v", rep)
	}
}

func TestSanitizePanicIsContainedAndFailsClosed(t *testing.T) {
	fs := &fakeSanitize{fn: func(*ev) error { panic("secret value in panic") }}
	c, _ := NewChain(Config[ev]{Sanitize: fs})
	e := &ev{Payload: "token=SECRET"}
	rep := c.Apply(t.Context(), e)
	if !errors.Is(rep.Err(), ErrPanic) || !rep.Emit() || strings.Contains(e.Payload, "SECRET") {
		t.Fatalf("report=%+v payload=%q", rep, e.Payload)
	}
	if strings.Contains(rep.Err().Error(), "secret value") {
		t.Fatalf("panic value leaked into the error: %v", rep.Err())
	}
}

type panickingFailClosed struct{ fakeSanitize }

func (panickingFailClosed) FailClosed(*ev, error) { panic("cannot scrub") }

func TestEventIsDroppedWhenFailClosedItselfPanics(t *testing.T) {
	p := &panickingFailClosed{fakeSanitize{fn: func(*ev) error { return errors.New("boom") }}}
	c, _ := NewChain(Config[ev]{Sanitize: p, Classify: trail("classify", Outcome{}, nil)})
	e := &ev{}
	rep := c.Apply(t.Context(), e)
	if rep.Emit() {
		t.Fatal("an event that could not be made safe must not be emitted")
	}
	if strings.Contains(strings.Join(e.Trail, ","), "classify") {
		t.Fatalf("classify ran on an unsafe event: %v", e.Trail)
	}
}

func TestStampAndClassifyErrorsDoNotStopTheEvent(t *testing.T) {
	c, _ := NewChain(Config[ev]{
		Stamp:    trail("stamp", Outcome{}, errors.New("no context")),
		Sanitize: &fakeSanitize{fn: scrub},
		Classify: Named("classify", func(context.Context, *ev) (Outcome, error) { panic("rule bug") }),
	})
	e := &ev{Payload: "x"}
	rep := c.Apply(t.Context(), e)
	if !rep.Emit() || !e.Sanitized {
		t.Fatalf("report=%+v sanitized=%v", rep, e.Sanitized)
	}
	if !errors.Is(rep.Err(), ErrPanic) {
		t.Fatalf("err = %v", rep.Err())
	}
}

func TestDroppedStopsTheChain(t *testing.T) {
	c, _ := NewChain(Config[ev]{
		Stamp:    trail("stamp", Outcome{Dropped: true}, nil),
		Sanitize: &fakeSanitize{fn: scrub},
	})
	e := &ev{}
	rep := c.Apply(t.Context(), e)
	if rep.Emit() || strings.Contains(strings.Join(e.Trail, ","), "sanitize") {
		t.Fatalf("report=%+v trail=%v", rep, e.Trail)
	}
}
