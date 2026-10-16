package panics

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

type sink struct {
	mu   sync.Mutex
	recs []Record
}

func (s *sink) Report(r Record) { s.mu.Lock(); s.recs = append(s.recs, r); s.mu.Unlock() }
func (s *sink) all() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.recs...)
}

const needle = "NEEDLE-VALUE-123"

func scrub(s string) string { return strings.ReplaceAll(s, needle, "[redacted]") }

func TestCallTurnsAPanicIntoAnErrorThatCarriesOnlyARef(t *testing.T) {
	s := &sink{}
	g := New(Config{Reporter: s, Redact: scrub})
	err := g.Call("test boundary", func() error { panic("the command held " + needle) })
	ref, ok := Ref(err)
	if !ok || !regexp.MustCompile(`^pnc_[0-9a-f]{12}$`).MatchString(ref) {
		t.Fatalf("%v", err)
	}
	if err.Error() != "internal error (ref "+ref+")" {
		t.Errorf("Error() must be the ref and nothing else: %q", err.Error())
	}
	recs := s.all()
	if len(recs) != 1 || recs[0].Ref != ref || recs[0].Where != "test boundary" {
		t.Fatalf("%+v", recs)
	}
	if strings.Contains(recs[0].Value, needle) || !strings.Contains(recs[0].Value, "[redacted]") {
		t.Errorf("value was not redacted: %q", recs[0].Value)
	}
	if !strings.Contains(recs[0].Stack, "panics") {
		t.Errorf("stack missing: %q", recs[0].Stack)
	}
}

func TestWithoutARedactorTheValueIsWithheld(t *testing.T) {
	s := &sink{}
	g := New(Config{Reporter: s}) // secure by default: no redactor, no panic text
	_ = g.Call("x", func() error { panic("user typed " + needle) })
	v := s.all()[0].Value
	if strings.Contains(v, needle) || !strings.Contains(v, "withheld") || !strings.Contains(v, "string") {
		t.Errorf("want the value withheld with only its type: %q", v)
	}
	if strings.Contains(s.all()[0].Stack, needle) {
		t.Error("the stack must not carry the value either")
	}
}

func TestUnredactedIsAnExplicitChoiceStillCleaned(t *testing.T) {
	s := &sink{}
	g := New(Config{Reporter: s, Redact: Unredacted})
	_ = g.Call("x", func() error { panic("raw " + needle + "\x1b[2J‮") })
	v := s.all()[0].Value
	if !strings.Contains(v, needle) || strings.ContainsAny(v, "\x1b‮") {
		t.Errorf("%q", v)
	}
}

func TestTheStackIsRedactedToo(t *testing.T) {
	s := &sink{}
	calls := 0
	g := New(Config{Reporter: s, Redact: func(x string) string { calls++; return x }})
	_ = g.Call("x", func() error { panic("v") })
	if calls < 3 { // where, value, stack all pass through it
		t.Errorf("redactor ran %d times; the value and stack must both go through it", calls)
	}
}

func TestAFailingRedactorNeverLeaksTheUnredactedText(t *testing.T) {
	s := &sink{}
	g := New(Config{Reporter: s, Redact: func(string) string { panic("redactor bug") }})
	err := g.Call("x", func() error { panic("contains " + needle) })
	if _, ok := Ref(err); !ok {
		t.Fatalf("not contained: %v", err)
	}
	r := s.all()[0]
	if strings.Contains(r.Value, needle) || strings.Contains(r.Stack, needle) || !strings.Contains(r.Value, "withheld") {
		t.Errorf("a broken redactor failed open: %q", r.Value)
	}
}

type evilError struct{}

func (evilError) Error() string { panic("Error() itself panics") }

type evilStringer struct{}

func (evilStringer) String() string { panic("String() itself panics") }

type cyclic struct{ next error }

func (c *cyclic) Error() string { return "cyc" }
func (c *cyclic) Unwrap() error { return c.next }

func TestHostilePanicValuesAreContained(t *testing.T) {
	s := &sink{}
	g := New(Config{Reporter: s, Redact: Unredacted, MaxValue: 64, MaxStack: 512})
	cyc := &cyclic{}
	cyc.next = cyc
	var nilPtr *int
	values := map[string]any{
		"error whose Error panics": error(evilError{}), "stringer that panics": evilStringer{}, "cyclic chain": cyc,
		"nil": nil, "typed nil pointer": nilPtr, "huge string": strings.Repeat("x", 1<<20),
		"control and bidi": "line\x1b[2J\r‮\x00end", "non-utf8": "\xff\xfe\xfd", "struct": struct{ A, B int }{1, 2},
		"wrapped error": errors.Join(errors.New("a"), errors.New("b")),
	}
	for name, v := range values {
		err := g.Call(name, func() error { panic(v) })
		if _, ok := Ref(err); !ok {
			t.Errorf("%s: not contained: %v", name, err)
		}
	}
	for _, r := range s.all() {
		if utf8.RuneCountInString(r.Value) > 65 || utf8.RuneCountInString(r.Stack) > 513 {
			t.Errorf("%s: not bounded (%d, %d)", r.Where, utf8.RuneCountInString(r.Value), utf8.RuneCountInString(r.Stack))
		}
		for _, ch := range r.Value + r.Stack {
			if ch != '\n' && ch != '\t' && (unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch)) {
				t.Errorf("%s: active character %U survived", r.Where, ch)
			}
		}
		if !utf8.ValidString(r.Value) || !utf8.ValidString(r.Stack) {
			t.Errorf("%s: invalid UTF-8 recorded", r.Where)
		}
	}
}

func TestAFailingReporterDoesNotCascade(t *testing.T) {
	g := New(Config{Reporter: ReporterFunc(func(Record) { panic("reporter is broken") })})
	err := g.Call("x", func() error { panic("first") })
	if _, ok := Ref(err); !ok {
		t.Fatalf("%v", err)
	}
	good := &sink{}
	m := Multi(ReporterFunc(func(Record) { panic("one bad reporter") }), good)
	_ = New(Config{Reporter: m}).Call("x", func() error { panic("y") })
	if len(good.all()) != 1 {
		t.Error("Multi must isolate a failing reporter from the others")
	}
}

func TestOrdinaryResultsPassThrough(t *testing.T) {
	g := New(Config{Reporter: &sink{}})
	want := errors.New("plain")
	if err := g.Call("x", func() error { return want }); !errors.Is(err, want) {
		t.Error(err)
	}
	if err := g.Call("x", func() error { return nil }); err != nil {
		t.Error(err)
	}
}

func TestGoKeepsTheProcessAliveAndOnPanicRuns(t *testing.T) {
	s := &sink{}
	g := New(Config{Reporter: s})
	got := make(chan *Error, 1)
	g.Go("worker", func() { panic("in a goroutine") }, func(pe *Error) { got <- pe })
	select {
	case pe := <-got:
		if pe.Rec.Where != "worker" {
			t.Errorf("%+v", pe.Rec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onPanic not called")
	}
	done := make(chan struct{})
	g.Go("worker2", func() { panic("again") }, func(*Error) { defer close(done); panic("handler too") })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called")
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(s.all()); n != 2 {
		t.Errorf("want 2 records, got %d", n)
	}
}

func TestRecoverHelperSetsTheNamedResult(t *testing.T) {
	s := &sink{}
	SetDefault(Config{Reporter: s})
	t.Cleanup(func() { SetDefault(Config{Reporter: ReporterFunc(func(Record) {})}) })
	f := func() (err error) {
		defer Recover("helper", &err, With("run", "r1"))
		panic("boom")
	}
	err := f()
	if _, ok := Ref(err); !ok {
		t.Fatalf("%v", err)
	}
	if r := s.all()[0]; r.Where != "helper" || r.Attrs["run"] != "r1" {
		t.Errorf("%+v", r)
	}
	g := func() (err error) {
		defer Recover("none", &err)
		return nil
	}
	if g() != nil {
		t.Error("no panic must leave the result alone")
	}
	h := func() { defer Recover("nil errp", nil); panic("still contained") }
	h()
}

func TestAttrsAreSanitized(t *testing.T) {
	s := &sink{}
	g := New(Config{Reporter: s, Redact: scrub})
	_ = g.Call("x", func() error { panic("p") }, With("run", "id\x1b[2J "+needle))
	v := s.all()[0].Attrs["run"]
	if strings.Contains(v, needle) || strings.Contains(v, "\x1b") {
		t.Errorf("%q", v)
	}
}

func TestGuardsAreIsolatedFromTheDefault(t *testing.T) {
	a, b := &sink{}, &sink{}
	ga, gb := New(Config{Reporter: a}), New(Config{Reporter: b})
	_ = ga.Call("a", func() error { panic(1) })
	_ = gb.Call("b", func() error { panic(2) })
	if len(a.all()) != 1 || len(b.all()) != 1 || a.all()[0].Where != "a" {
		t.Errorf("%v %v", a.all(), b.all())
	}
}

func TestConcurrentCapturesGetDistinctRefs(t *testing.T) {
	s := &sink{}
	g := New(Config{Reporter: s})
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = g.Call("c", func() error { panic("x") }) }()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, r := range s.all() {
		if seen[r.Ref] {
			t.Fatalf("duplicate ref %s", r.Ref)
		}
		seen[r.Ref] = true
	}
	if len(seen) != 200 {
		t.Errorf("%d refs", len(seen))
	}
}

func TestCleanBoundsWithoutSplittingARune(t *testing.T) {
	out := Clean(strings.Repeat("é€😀", 100), 7)
	if !utf8.ValidString(out) || utf8.RuneCountInString(out) != 8 { // 7 runes + the ellipsis
		t.Errorf("%q (%d runes)", out, utf8.RuneCountInString(out))
	}
	if got := Clean("a\tb\nc\x00d​e", 50); got != "a\tb\nc d e" {
		t.Errorf("%q", got)
	}
	if !utf8.ValidString(Clean("\xff\xfe", 10)) {
		t.Error("invalid UTF-8 must be repaired")
	}
}

func FuzzCapture(f *testing.F) {
	for _, s := range []string{"", "plain", "x\x1b[2J", "‮", "\xff\xfe", strings.Repeat("a", 5000), needle} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		s := &sink{}
		g := New(Config{Reporter: s, Redact: Unredacted, MaxValue: 100, MaxStack: 200})
		err := g.Call("fuzz", func() error { panic(in) })
		if _, ok := Ref(err); !ok {
			t.Fatalf("not contained")
		}
		if in != "" && len(in) > 3 && strings.Contains(err.Error(), in) {
			t.Fatalf("Error() leaked the value")
		}
		r := s.all()[0]
		if !utf8.ValidString(r.Value) || utf8.RuneCountInString(r.Value) > 101 {
			t.Fatalf("value invalid or unbounded: %q", r.Value)
		}
		for _, ch := range r.Value {
			if ch != '\n' && ch != '\t' && (unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch)) {
				t.Fatalf("active character %U", ch)
			}
		}
	})
}
