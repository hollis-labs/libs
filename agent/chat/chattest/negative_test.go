package chattest_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/agent/chat/chattest"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
)

// recorder is a testing.TB that notes failures instead of failing.
type recorder struct {
	testing.TB
	failures []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...) }
func (r *recorder) Fatal(args ...any)                 { r.failures = append(r.failures, fmt.Sprint(args...)) }

func events(steps []chattest.Step) []chatstream.Event {
	out := make([]chatstream.Event, len(steps))
	for i, s := range steps {
		out[i] = s.Event
	}
	return out
}

// The suite must fail the runners the rules exclude; a suite that passes
// everything checks nothing.
func TestCheckRunRejectsRuleBreakers(t *testing.T) {
	strip := func(evs []chatstream.Event, verb chatstream.Verb, partID string) []chatstream.Event {
		out := append([]chatstream.Event(nil), evs...)
		for i := range out {
			if out[i].Verb == verb && (partID == "" || out[i].PartID == partID) {
				out[i].Final = nil
			}
		}
		return out
	}
	good := events(chattest.DefaultScript(chatTurn("tool")))
	var noDeltas []chatstream.Event
	for _, ev := range good {
		if ev.Verb != chatstream.VerbPartDelta {
			noDeltas = append(noDeltas, ev)
		}
	}
	user := append([]chatstream.Event(nil), good...)
	user[2].Role = "user"
	seq := append([]chatstream.Event(nil), good...)
	seq[1].Seq = 7

	for _, tc := range []struct {
		name string
		evs  []chatstream.Event
		want string // a failure must mention it; "" means the run must pass
	}{
		{"a good run", good, ""},
		{"a good run without deltas", noDeltas, ""},
		{"streamed text with no final is the Service's to fill", strip(good, chatstream.VerbPartEnd, "p1"), ""},
		{"a whole tool_result with no final", strip(good, chatstream.VerbPartEnd, "r1"), `"r1"`},
		{"a whole tool_call with no final", strip(good, chatstream.VerbPartEnd, "c1"), `"c1"`},
		{"whole text with no final", strip(noDeltas, chatstream.VerbPartEnd, "p1"), `"p1"`},
		{"no run.start", good[1:], "run.start"},
		{"no terminal event", good[:len(good)-1], "terminal"},
		{"an event after the terminal", append(append([]chatstream.Event(nil), good...), good[len(good)-1]), "terminal"},
		{"a part left open", append(append([]chatstream.Event(nil), good[:6]...), good[len(good)-1]), "open"},
		{"the user's message", user, "user"},
		{"a seq of its own", seq, "seq"},
		{"nothing at all", nil, "no events"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{TB: t}
			chattest.CheckRun(r, tc.evs)
			all := strings.Join(r.failures, "\n")
			switch {
			case tc.want == "" && len(r.failures) > 0:
				t.Fatalf("a conforming run was failed:\n%s", all)
			case tc.want != "" && !strings.Contains(all, tc.want):
				t.Fatalf("want a failure mentioning %q, got:\n%s", tc.want, all)
			}
		})
	}
}
