package chattest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
)

// Step is one move of a script: an event to emit, or a point where the run
// waits. Build steps with the functions below.
type Step struct {
	// Event is emitted as is. The runner fills in the schema version and the
	// time; the Service sets the run_id.
	Event chatstream.Event
	// Gate, when set, makes the run wait there until [Scripted.Release] lets
	// one run through the gate of that name (or the run is canceled). No event
	// is emitted.
	Gate string
	// OnAllow and OnDeny continue the script after an approval.request, once
	// it is answered.
	OnAllow, OnDeny []Step
	approval        bool
}

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("chattest: " + err.Error())
	}
	return b
}

// Join concatenates step lists.
func Join(parts ...[]Step) []Step {
	var out []Step
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// Start is run.start.
func Start(provider, model string) []Step {
	return []Step{{Event: chatstream.Event{Verb: chatstream.VerbRunStart, Provider: provider, Model: model}}}
}

// Message wraps parts in message.start and message.end with role assistant.
func Message(id string, parts ...[]Step) []Step {
	out := []Step{{Event: chatstream.Event{Verb: chatstream.VerbMessageStart, MessageID: id, Role: "assistant"}}}
	out = append(out, Join(parts...)...)
	return append(out, Step{Event: chatstream.Event{Verb: chatstream.VerbMessageEnd, MessageID: id}})
}

// ModelStep wraps steps in step.start and step.finish.
func ModelStep(id string, inner ...[]Step) []Step {
	out := []Step{{Event: chatstream.Event{Verb: chatstream.VerbStepStart, StepID: id}}}
	out = append(out, Join(inner...)...)
	return append(out, Step{Event: chatstream.Event{Verb: chatstream.VerbStepFinish, StepID: id}})
}

func streamed(id string, kind chatstream.PartKind, chunks []string) []Step {
	out := []Step{{Event: chatstream.Event{Verb: chatstream.VerbPartStart, PartID: id, Kind: string(kind)}}}
	for _, c := range chunks {
		out = append(out, Step{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: id, Text: c}})
	}
	final := raw(map[string]string{"text": strings.Join(chunks, "")})
	return append(out, Step{Event: chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: id, Final: final}})
}

// Text is a text part streamed as the given chunks. Its part.end carries the
// whole text in final, so the same script serves the whole-message mode.
func Text(id string, chunks ...string) []Step { return streamed(id, chatstream.PartText, chunks) }

// Reasoning is a reasoning part streamed as the given chunks.
func Reasoning(id string, chunks ...string) []Step {
	return streamed(id, chatstream.PartReasoning, chunks)
}

// ToolCall is a whole tool_call part: name, and its full input as final.
func ToolCall(id, name string, input any) []Step {
	meta := map[string]json.RawMessage{chatstream.MetaName: raw(name)}
	return []Step{
		{Event: chatstream.Event{Verb: chatstream.VerbPartStart, PartID: id, Kind: string(chatstream.PartToolCall), Meta: meta}},
		{Event: chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: id, Final: raw(input)}},
	}
}

// ToolResult is a whole tool_result part answering the tool_call part callID.
func ToolResult(id, callID, text string, isError bool) []Step {
	meta := map[string]json.RawMessage{chatstream.MetaCallID: raw(callID), chatstream.MetaIsError: raw(isError)}
	return []Step{
		{Event: chatstream.Event{Verb: chatstream.VerbPartStart, PartID: id, Kind: string(chatstream.PartToolResult), Meta: meta}},
		{Event: chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: id, Final: raw(map[string]any{"text": text, "is_error": isError})}},
	}
}

// Approval emits an approval.request and waits for its answer, then continues
// with onAllow or onDeny. standing false marks a tool that asks every time.
func Approval(id, callID, description string, standing bool, onAllow, onDeny []Step) []Step {
	return []Step{{
		Event: chatstream.Event{
			Verb: chatstream.VerbApprovalRequest, ApprovalID: id, CallID: callID, Reason: description,
			Mode:       chatstream.ApprovalInBand,
			Descriptor: raw(map[string]any{"description": description, "standing": standing}),
		},
		OnAllow: onAllow, OnDeny: onDeny, approval: true,
	}}
}

// Gate makes the run wait until Scripted.Release(name) lets it through.
func Gate(name string) []Step { return []Step{{Gate: name}} }

// Activity is an activity event.
func Activity(kind string, value any) []Step {
	return []Step{{Event: chatstream.Event{Verb: chatstream.VerbActivity, Kind: kind, Value: raw(value)}}}
}

// Finish is run.finish.
func Finish(reason chatstream.FinishReason) []Step {
	return []Step{{Event: chatstream.Event{Verb: chatstream.VerbRunFinish, Reason: string(reason)}}}
}

// Fail is run.error.
func Fail(code, message string) []Step {
	return []Step{{Event: chatstream.Event{Verb: chatstream.VerbRunError, Code: code, Message: message}}}
}

// Abort is run.abort.
func Abort(reason string) []Step {
	return []Step{{Event: chatstream.Event{Verb: chatstream.VerbRunAbort, Reason: reason}}}
}

// Reply is the plain script: one assistant message whose text is streamed in
// the given chunks, ending with run.finish (stop).
func Reply(chunks ...string) []Step {
	return Join(Start("scripted", "script-1"), Message("m1", Text("p1", chunks...)), Finish(chatstream.FinishStop))
}

// DefaultScript picks a script from the first word of the turn's text, so one
// runner can show every shape of run to a client under development:
//
//	tool       a tool call and its result, then an answer
//	approval   an approval.request; allow runs the tool, deny says so
//	confirm    the same with standing false (offer once and deny only)
//	reasoning  a reasoning part, then an answer
//	error      a partial answer, then run.error
//	abort      a partial answer, then run.abort
//	length     an answer cut off: run.finish with reason length
//	hold       starts an answer and waits to be canceled
//
// Anything else is echoed back, streamed word by word.
func DefaultScript(turn chat.Turn) []Step {
	first, _, _ := strings.Cut(strings.TrimSpace(turn.Text), " ")
	start := Start("scripted", "script-1")
	stop := Finish(chatstream.FinishStop)
	toolRun := func(call []Step) []Step {
		return Join(start,
			ModelStep("s1", Message("m1", Text("p1", "Let me ", "look that up."), call)),
			ModelStep("s2", Message("m2", ToolResult("r1", "c1", "42 results", false), Text("p2", "I found ", "42 results."))),
			stop)
	}
	lookup := ToolCall("c1", "lookup", map[string]string{"query": "everything"})
	ask := func(standing bool) []Step {
		allow := Join(
			[]Step{{Event: chatstream.Event{Verb: chatstream.VerbMessageEnd, MessageID: "m1"}}},
			Message("m2", ToolResult("r1", "c1", "written", false), Text("p2", "Done: ", "the file is written.")), stop)
		deny := Join(
			[]Step{{Event: chatstream.Event{Verb: chatstream.VerbMessageEnd, MessageID: "m1"}}},
			Message("m2", ToolResult("r1", "c1", "permission denied", true), Text("p2", "I did not ", "write the file.")), stop)
		return Join(start,
			[]Step{{Event: chatstream.Event{Verb: chatstream.VerbMessageStart, MessageID: "m1", Role: "assistant"}}},
			ToolCall("c1", "write_file", map[string]string{"path": "notes.txt"}),
			Approval("ap1", "c1", "Write notes.txt", standing, allow, deny))
	}
	switch strings.ToLower(first) {
	case "tool":
		return toolRun(lookup)
	case "approval":
		return ask(true)
	case "confirm":
		return ask(false)
	case "reasoning":
		return Join(start, Message("m1", Reasoning("p1", "The user wants ", "an answer."), Text("p2", "Here ", "it is.")), stop)
	case "error":
		return Join(start, Message("m1", Text("p1", "This will ", "not end well.")), Fail("upstream_error", "the scripted provider failed"))
	case "abort":
		return Join(start, Message("m1", Text("p1", "Stopping ", "here.")), Abort("scripted"))
	case "length":
		return Join(start, Message("m1", Text("p1", "This answer is cut ", "off")), Finish(chatstream.FinishLength))
	case "hold":
		return Join(start,
			[]Step{
				{Event: chatstream.Event{Verb: chatstream.VerbMessageStart, MessageID: "m1", Role: "assistant"}},
				{Event: chatstream.Event{Verb: chatstream.VerbPartStart, PartID: "p1", Kind: string(chatstream.PartText)}},
				{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: "p1", Text: "Working"}},
			},
			Gate("hold"),
			[]Step{
				{Event: chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: "p1", Text: " and done."}},
				{Event: chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: "p1"}},
				{Event: chatstream.Event{Verb: chatstream.VerbMessageEnd, MessageID: "m1"}},
			}, stop)
	}
	words := strings.SplitAfter("You said: "+turn.Text, " ")
	return Reply(words...)
}

// ScriptedOption configures a [Scripted] runner.
type ScriptedOption func(*Scripted)

// WithScript sets how a turn chooses its script (default [DefaultScript]).
func WithScript(pick func(chat.Turn) []Step) ScriptedOption {
	return func(s *Scripted) { s.pick = pick }
}

// Whole makes the runner yield whole messages: every part.delta is left out
// and Info reports Deltas false. Scripts built from this package's functions
// carry final on every part.end, so the content is all still there.
func Whole() ScriptedOption { return func(s *Scripted) { s.whole = true } }

// WithDelay waits between events, so a person watching a stream sees it
// arrive. Tests leave it at zero.
func WithDelay(d time.Duration) ScriptedOption { return func(s *Scripted) { s.delay = d } }

// WithoutApprovals makes Info report Approvals false.
func WithoutApprovals() ScriptedOption { return func(s *Scripted) { s.noApprovals = true } }

// Recorded is one call of Runner.Record.
type Recorded struct {
	Turn   chat.Turn
	Answer string
}

// Scripted is a chat.Runner that plays scripts back. It is safe for
// concurrent use.
type Scripted struct {
	pick        func(chat.Turn) []Step
	whole       bool
	noApprovals bool
	delay       time.Duration
	replaced    chan string

	mu       sync.Mutex
	gates    map[string]chan struct{}
	turns    []chat.Turn
	recorded []Recorded
}

var _ chat.Runner = (*Scripted)(nil)

// NewScripted returns a scripted runner.
func NewScripted(opts ...ScriptedOption) *Scripted {
	s := &Scripted{pick: DefaultScript, replaced: make(chan string, 16), gates: map[string]chan struct{}{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Start implements chat.Runner.
func (s *Scripted) Start(_ context.Context, turn chat.Turn) (chat.Run, error) {
	s.mu.Lock()
	s.turns = append(s.turns, turn)
	s.mu.Unlock()
	return s.Play(s.pick(turn)), nil
}

// Play returns a run that plays steps, without a turn: the run to hand to
// chat.Service.Observe.
func (s *Scripted) Play(steps []Step) chat.Run {
	return &scriptedRun{s: s, steps: steps, cancel: make(chan struct{}), answers: map[string]chan chat.Decision{}}
}

// Info implements chat.Runner.
func (s *Scripted) Info(_ context.Context, conversationID string) (chat.Info, error) {
	return chat.Info{Deltas: !s.whole, Approvals: !s.noApprovals, RunnerRef: "scripted:" + conversationID}, nil
}

// Record implements chat.Runner.
func (s *Scripted) Record(_ context.Context, turn chat.Turn, answer string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded = append(s.recorded, Recorded{Turn: turn, Answer: answer})
	return nil
}

// Replaced implements chat.Runner.
func (s *Scripted) Replaced() <-chan string { return s.replaced }

// Replace signals that the runner's side of a conversation was replaced.
func (s *Scripted) Replace(conversationID string) { s.replaced <- conversationID }

// Turns returns the turns Start was given, in order.
func (s *Scripted) Turns() []chat.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]chat.Turn(nil), s.turns...)
}

// Recorded returns the calls of Record, in order.
func (s *Scripted) Recorded() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Recorded(nil), s.recorded...)
}

func (s *Scripted) gate(name string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.gates[name]
	if g == nil {
		g = make(chan struct{}, 1024)
		s.gates[name] = g
	}
	return g
}

// Release lets one run through the named gate: the run waiting there now, or
// the next one to reach it. A release is never lost to timing.
func (s *Scripted) Release(name string) { s.gate(name) <- struct{}{} }

type scriptedRun struct {
	s      *Scripted
	steps  []Step
	cancel chan struct{}
	once   sync.Once

	mu      sync.Mutex
	answers map[string]chan chat.Decision
}

// Cancel implements chat.Run.
func (r *scriptedRun) Cancel() { r.once.Do(func() { close(r.cancel) }) }

// Answer implements chat.Run.
func (r *scriptedRun) Answer(ctx context.Context, approvalID string, d chat.Decision) error {
	r.mu.Lock()
	ch := r.answers[approvalID]
	delete(r.answers, approvalID)
	r.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("chattest: no approval %q is waiting", approvalID)
	}
	select {
	case ch <- d:
		return nil
	case <-r.cancel:
		return errCanceled
	case <-ctx.Done():
		return ctx.Err()
	}
}

// player tracks what is open, so a canceled run can close it before run.abort.
type player struct {
	parts    []string
	text     map[string]*strings.Builder
	messages []string
	steps    []string
}

func (p *player) note(ev chatstream.Event) {
	drop := func(list []string, id string) []string {
		for i, v := range list {
			if v == id {
				return append(list[:i:i], list[i+1:]...)
			}
		}
		return list
	}
	switch ev.Verb { //nolint:exhaustive // only the bracketing verbs open or close anything
	case chatstream.VerbPartStart:
		p.parts = append(p.parts, ev.PartID)
		p.text[ev.PartID] = &strings.Builder{}
	case chatstream.VerbPartDelta:
		if b := p.text[ev.PartID]; b != nil {
			b.WriteString(ev.Text)
		}
	case chatstream.VerbPartEnd:
		p.parts = drop(p.parts, ev.PartID)
	case chatstream.VerbMessageStart:
		p.messages = append(p.messages, ev.MessageID)
	case chatstream.VerbMessageEnd:
		p.messages = drop(p.messages, ev.MessageID)
	case chatstream.VerbStepStart:
		p.steps = append(p.steps, ev.StepID)
	case chatstream.VerbStepFinish:
		p.steps = drop(p.steps, ev.StepID)
	}
}

// closing is what a canceled run emits: every open part, message and step
// closed, then run.abort.
func (p *player) closing() []chatstream.Event {
	var out []chatstream.Event
	for i := len(p.parts) - 1; i >= 0; i-- {
		id := p.parts[i]
		out = append(out, chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: id, Final: raw(map[string]string{"text": p.text[id].String()})})
	}
	for i := len(p.messages) - 1; i >= 0; i-- {
		out = append(out, chatstream.Event{Verb: chatstream.VerbMessageEnd, MessageID: p.messages[i]})
	}
	for i := len(p.steps) - 1; i >= 0; i-- {
		out = append(out, chatstream.Event{Verb: chatstream.VerbStepFinish, StepID: p.steps[i]})
	}
	return append(out, chatstream.Event{Verb: chatstream.VerbRunAbort, Reason: "canceled"})
}

var errCanceled = errors.New("chattest: run canceled")

// Events implements chat.Run.
func (r *scriptedRun) Events() iter.Seq2[chatstream.Event, error] {
	return func(yield func(chatstream.Event, error) bool) {
		p := &player{text: map[string]*strings.Builder{}}
		emit := func(ev chatstream.Event) bool {
			ev.V, ev.Time = chatstream.SchemaVersion, time.Now()
			if b := p.text[ev.PartID]; r.s.whole && ev.Verb == chatstream.VerbPartEnd && len(ev.Final) == 0 && b != nil {
				// Nothing was streamed, so nobody downstream can build final.
				ev.Final = raw(map[string]string{"text": b.String()})
			}
			p.note(ev)
			return yield(ev, nil)
		}
		// wait blocks on ch, or returns errCanceled.
		wait := func(ch <-chan struct{}) error {
			select {
			case <-ch:
				return nil
			case <-r.cancel:
				return errCanceled
			}
		}
		queue := r.steps
		for len(queue) > 0 {
			st := queue[0]
			queue = queue[1:]
			var err error
			select {
			case <-r.cancel:
				err = errCanceled
			default:
			}
			var decided chan chat.Decision
			switch {
			case err != nil:
			case st.Gate != "":
				err = wait(r.s.gate(st.Gate))
			case st.Event.Verb == chatstream.VerbPartDelta && r.s.whole:
				p.note(st.Event) // kept for the text, not emitted
			default:
				if r.s.delay > 0 {
					t := time.NewTimer(r.s.delay)
					select {
					case <-t.C:
					case <-r.cancel:
						t.Stop()
						err = errCanceled
					}
				}
				if err != nil {
					break
				}
				if st.approval {
					decided = make(chan chat.Decision)
					r.mu.Lock()
					r.answers[st.Event.ApprovalID] = decided
					r.mu.Unlock()
				}
				if !emit(st.Event) {
					return
				}
				if st.Event.IsTerminal() {
					return
				}
			}
			if err == nil && decided != nil {
				select {
				case d := <-decided:
					if d.Allow {
						queue = st.OnAllow
					} else {
						queue = st.OnDeny
					}
				case <-r.cancel:
					err = errCanceled
				}
			}
			if err != nil {
				for _, ev := range p.closing() {
					if !emit(ev) {
						return
					}
				}
				return
			}
		}
	}
}
