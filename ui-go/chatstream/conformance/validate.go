package conformance

import (
	"encoding/json"
	"fmt"
	"testing"
	"unicode/utf8"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
)

// Violation is one broken invariant.
type Violation struct {
	// Index is the position in the stream of the offending event; -1 for a
	// violation of the stream as a whole (a missing terminal event).
	Index int
	Seq   uint64
	Rule  string
	Msg   string
}

func (v Violation) String() string {
	if v.Index < 0 {
		return fmt.Sprintf("%s: %s", v.Rule, v.Msg)
	}
	return fmt.Sprintf("event %d (seq %d): %s: %s", v.Index, v.Seq, v.Rule, v.Msg)
}

// The rule names Validate reports.
const (
	RuleTerminal   = "terminal"
	RuleLifecycle  = "lifecycle"
	RulePayload    = "payload"
	RuleUsage      = "usage"
	RuleVocabulary = "vocabulary"
	RuleEnvelope   = "envelope"
	// RuleConvention: a part.start lacks the Meta a consumer cannot render
	// without (chatstream.MetaName on tool_call, MetaCallID on tool_result).
	RuleConvention = "convention"
)

// Options tune Validate.
type Options struct {
	// AllowGap accepts gap events, which only a hub or framing layer produces.
	// Validate rejects them by default, because a decoder must never emit one.
	AllowGap bool
	// AllowOpenAtEnd accepts parts, messages or steps still open at the
	// terminal event. Off by default: a terminal event closes nothing.
	AllowOpenAtEnd bool
	// IncompleteOK accepts a stream with no terminal event (a live stream
	// snapshotted mid-run). Off by default.
	IncompleteOK bool
}

// Validate checks events against the invariants above and returns every
// violation, in stream order. An empty result means the stream is well formed.
func Validate(events []chatstream.Event, opts ...Options) []Violation {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	v := &validator{o: o, parts: map[string]partState{}}
	for i, ev := range events {
		v.event(i, ev)
	}
	v.end(len(events))
	return v.out
}

// Check fails t for every violation Validate finds.
func Check(t testing.TB, events []chatstream.Event, opts ...Options) {
	t.Helper()
	for _, viol := range Validate(events, opts...) {
		t.Errorf("invalid stream: %s", viol)
	}
}

type partState struct {
	kind chatstream.PartKind
	open bool
}

type validator struct {
	o          Options
	out        []Violation
	runID      string
	started    bool
	terminal   bool
	terminalAt int
	lastSeq    uint64
	parts      map[string]partState
	msgOpen    bool
	steps      map[string]bool
	finalUsage int
	cumulative *chatstream.Usage
}

func (v *validator) add(i int, ev chatstream.Event, rule, format string, args ...any) {
	v.out = append(v.out, Violation{Index: i, Seq: ev.Seq, Rule: rule, Msg: fmt.Sprintf(format, args...)})
}

func (v *validator) event(i int, ev chatstream.Event) {
	if v.terminal {
		v.add(i, ev, RuleTerminal, "%s follows the terminal event at index %d", ev.Verb, v.terminalAt)
		return
	}
	if ev.V != chatstream.SchemaVersion {
		v.add(i, ev, RuleEnvelope, "schema version %q, want %q", ev.V, chatstream.SchemaVersion)
	}
	if ev.Time.IsZero() {
		v.add(i, ev, RuleEnvelope, "no time")
	}
	if ev.Seq != 0 {
		if ev.Seq <= v.lastSeq {
			v.add(i, ev, RuleEnvelope, "seq %d does not increase past %d", ev.Seq, v.lastSeq)
		}
		v.lastSeq = ev.Seq
	}
	switch {
	case v.runID == "":
		v.runID = ev.RunID
	case ev.RunID != v.runID:
		v.add(i, ev, RuleEnvelope, "run id %q, stream is %q", ev.RunID, v.runID)
	}
	if !ev.Verb.Known() {
		v.add(i, ev, RuleVocabulary, "unknown verb %q", ev.Verb)
		return
	}
	if i > 0 && ev.Verb == chatstream.VerbRunStart {
		v.add(i, ev, RuleLifecycle, "run.start is not the first event")
	}
	if i == 0 && ev.Verb != chatstream.VerbRunStart && !ev.Verb.Terminal() {
		v.add(i, ev, RuleLifecycle, "the stream does not begin with run.start (%s)", ev.Verb)
	}
	if !utf8.ValidString(ev.Text) || !utf8.ValidString(ev.JSONFragment) || !utf8.ValidString(ev.Message) {
		v.add(i, ev, RulePayload, "text is not valid UTF-8")
	}
	v.jsonFields(i, ev)
	switch ev.Verb {
	case chatstream.VerbRunStart:
		v.started = true
	case chatstream.VerbRunFinish:
		if !ev.Finish().Known() {
			v.add(i, ev, RuleVocabulary, "finish reason %q is not in the closed vocabulary", ev.Reason)
		}
		if ev.Usage != nil {
			v.usage(i, ev, *ev.Usage, chatstream.UsageFinal)
		}
		v.finishTerminal(i, ev)
	case chatstream.VerbRunError:
		if ev.Code == "" {
			v.add(i, ev, RulePayload, "run.error without a code")
		}
		v.finishTerminal(i, ev)
	case chatstream.VerbRunAbort:
		v.finishTerminal(i, ev)
	case chatstream.VerbStepStart:
		if v.steps == nil {
			v.steps = map[string]bool{}
		}
		if v.steps[ev.StepID] {
			v.add(i, ev, RuleLifecycle, "step %q is already open", ev.StepID)
		}
		v.steps[ev.StepID] = true
	case chatstream.VerbStepFinish:
		if !v.steps[ev.StepID] {
			v.add(i, ev, RuleLifecycle, "step.finish for step %q that is not open", ev.StepID)
		}
		delete(v.steps, ev.StepID)
	case chatstream.VerbMessageStart:
		if v.msgOpen {
			v.add(i, ev, RuleLifecycle, "message.start inside an open message")
		}
		v.msgOpen = true
	case chatstream.VerbMessageEnd:
		if !v.msgOpen {
			v.add(i, ev, RuleLifecycle, "message.end with no open message")
		}
		v.msgOpen = false
	case chatstream.VerbPartStart:
		if ev.PartID == "" {
			v.add(i, ev, RulePayload, "part.start without a part id")
		}
		if st, ok := v.parts[ev.PartID]; ok && st.open {
			v.add(i, ev, RuleLifecycle, "part %q is already open (no id reuse while open)", ev.PartID)
		}
		if !ev.PartKind().Known() {
			v.add(i, ev, RuleVocabulary, "part kind %q is not in the vocabulary", ev.Kind)
		}
		switch ev.PartKind() { //nolint:exhaustive // only these two kinds carry a required convention
		case chatstream.PartToolCall:
			if ev.MetaString(chatstream.MetaName) == "" {
				v.add(i, ev, RuleConvention, "tool_call part %q has no meta %q (the tool's name)", ev.PartID, chatstream.MetaName)
			}
		case chatstream.PartToolResult:
			call := ev.MetaString(chatstream.MetaCallID)
			switch st, known := v.parts[call]; {
			case call == "":
				v.add(i, ev, RuleConvention, "tool_result part %q has no meta %q (the tool_call part it answers)", ev.PartID, chatstream.MetaCallID)
			case !known || st.kind != chatstream.PartToolCall:
				v.add(i, ev, RuleConvention, "tool_result part %q answers %q, which is not an earlier tool_call part", ev.PartID, call)
			}
		}
		v.parts[ev.PartID] = partState{kind: ev.PartKind(), open: true}
	case chatstream.VerbPartDelta:
		st, ok := v.parts[ev.PartID]
		switch {
		case !ok || !st.open:
			v.add(i, ev, RuleLifecycle, "part.delta for part %q that is not open", ev.PartID)
		case ev.Text != "" && ev.JSONFragment != "":
			v.add(i, ev, RulePayload, "part.delta carries both text and json_fragment")
		case ev.JSONFragment != "" && st.kind != chatstream.PartToolCall:
			v.add(i, ev, RulePayload, "json_fragment on a %s part (no mixed text and tool arguments in one part)", st.kind)
		case ev.Text != "" && st.kind == chatstream.PartToolCall:
			v.add(i, ev, RulePayload, "text on a tool_call part (no mixed text and tool arguments in one part)")
		}
	case chatstream.VerbPartEnd:
		st, ok := v.parts[ev.PartID]
		if !ok || !st.open {
			v.add(i, ev, RuleLifecycle, "part.end for part %q that is not open", ev.PartID)
		}
		st.open = false
		v.parts[ev.PartID] = st
	case chatstream.VerbApprovalRequest:
		if ev.ApprovalID == "" {
			v.add(i, ev, RulePayload, "approval.request without an approval id")
		}
		if ev.Mode != chatstream.ApprovalInBand && ev.Mode != chatstream.ApprovalSuspend {
			v.add(i, ev, RuleVocabulary, "approval mode %q", ev.Mode)
		}
	case chatstream.VerbUsage:
		if ev.Usage == nil {
			v.add(i, ev, RulePayload, "usage event without usage")
			break
		}
		v.usage(i, ev, *ev.Usage, ev.Usage.Scope)
	case chatstream.VerbActivity:
		if ev.Kind == "" {
			v.add(i, ev, RulePayload, "activity without a kind")
		}
	case chatstream.VerbRaw:
		if ev.Raw == nil || ev.Raw.Dialect == "" {
			v.add(i, ev, RulePayload, "raw event without raw.dialect")
		}
	case chatstream.VerbGap:
		if !v.o.AllowGap {
			v.add(i, ev, RuleLifecycle, "gap event from a decoder (gaps come from the hub)")
		}
	}
}

// jsonFields reports every raw JSON field of ev that is not valid JSON: an
// encoder splices these into its output as they are.
func (v *validator) jsonFields(i int, ev chatstream.Event) {
	check := func(name string, raw json.RawMessage) {
		if len(raw) > 0 && !json.Valid(raw) {
			v.add(i, ev, RulePayload, "%s is not valid JSON", name)
		}
	}
	check("final", ev.Final)
	check("descriptor", ev.Descriptor)
	check("value", ev.Value)
	check("patch", ev.Patch)
	for k, raw := range ev.Meta {
		check("meta "+k, raw)
	}
	for k, raw := range ev.Ext {
		check("ext "+k, raw)
	}
	if ev.Raw != nil {
		check("raw.payload", ev.Raw.Payload)
	}
}

func (v *validator) usage(i int, ev chatstream.Event, u chatstream.Usage, scope chatstream.UsageScope) {
	if err := u.Valid(); err != nil {
		v.add(i, ev, RuleUsage, "%v", err)
	}
	switch scope {
	case chatstream.UsageFinal:
		v.finalUsage++
		if v.finalUsage > 1 {
			v.add(i, ev, RuleUsage, "more than one final usage")
		}
	case chatstream.UsageCumulative:
		if v.cumulative != nil {
			p := *v.cumulative
			if u.UncachedInput < p.UncachedInput || u.CacheRead < p.CacheRead || u.CacheWrite < p.CacheWrite ||
				u.Output < p.Output || u.Reasoning < p.Reasoning {
				v.add(i, ev, RuleUsage, "cumulative usage went down: %+v after %+v", u, p)
			}
		}
		cu := u
		v.cumulative = &cu
	case chatstream.UsageDelta:
	default:
		v.add(i, ev, RuleVocabulary, "usage scope %q", scope)
	}
}

func (v *validator) finishTerminal(i int, ev chatstream.Event) {
	v.terminal, v.terminalAt = true, i
	if v.o.AllowOpenAtEnd {
		return
	}
	for id, st := range v.parts {
		if st.open {
			v.add(i, ev, RuleLifecycle, "part %q is still open at the terminal event", id)
		}
	}
	if v.msgOpen {
		v.add(i, ev, RuleLifecycle, "a message is still open at the terminal event")
	}
	for id := range v.steps {
		v.add(i, ev, RuleLifecycle, "step %q is still open at the terminal event", id)
	}
}

func (v *validator) end(n int) {
	if !v.terminal && !v.o.IncompleteOK {
		v.out = append(v.out, Violation{Index: -1, Rule: RuleTerminal, Msg: "the stream has no terminal event"})
	}
	_ = n
}
