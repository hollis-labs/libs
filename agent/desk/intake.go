package desk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// IntakeMode is how far the front desk acts on its plans.
type IntakeMode string

const (
	// IntakeOff skips the front desk: every message goes to the chat agent.
	IntakeOff IntakeMode = "off"
	// IntakeShadow plans every message and records the plan, but shows and
	// does nothing. Its plans are for measuring the planner.
	IntakeShadow IntakeMode = "shadow"
	// IntakeSuggest shows a non-trivial plan to the owner as a card and
	// runs it only once the owner accepts (or edits) it.
	IntakeSuggest IntakeMode = "suggest"
	// IntakeAuto runs a confident plan without asking.
	IntakeAuto IntakeMode = "auto"
)

func (m IntakeMode) valid() bool {
	switch m {
	case IntakeOff, IntakeShadow, IntakeSuggest, IntakeAuto:
		return true
	}
	return false
}

// Request is one owner message for the front desk to read.
type Request struct {
	Message string
	Mode    string  // the deliberate mode, if any
	Topics  []Topic // open topics the message may continue
}

// Decomposer turns a message into a plan.
type Decomposer interface {
	Decompose(ctx context.Context, req Request) (Plan, error)
}

// Screening is the cheap first read of a message: is it worth a planner?
type Screening struct {
	MultiPart bool     `json:"multi_part"`
	Task      bool     `json:"task"` // asks for work, not just an answer
	Parts     []string `json:"parts,omitempty"`
	Reasons   []string `json:"reasons,omitempty"`
}

// Worth reports whether the message should go to the planner.
func (s Screening) Worth() bool { return s.MultiPart || s.Task }

var (
	listLine   = regexp.MustCompile(`(?m)^\s*(?:[-*•]|\d+[.)])\s+(\S.*)$`)
	sentence   = regexp.MustCompile(`[^.?!\n]+[?]`)
	urls       = regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://\S+`)
	taskVerbs  = regexp.MustCompile(`(?i)\b(research|investigate|look into|dig into|benchmark|audit|track down|file (?:a |the )?task|create (?:a )?tasks?|open (?:a )?(?:pr|pull request|issue)|ask (?:the )?\w+ (?:agent|seat|team)|have (?:the )?\w+ (?:agent|seat|team)|tell (?:the )?\w+ (?:agent|seat|team)|coordinate with)\b`)
	alsoJoiner = regexp.MustCompile(`(?i)(?:\band also\b|\balso,|\bseparately,|\bon another note\b|\bone more thing\b|\bsecond(?:ly)?,|\bthen,? (?:can|could) you\b)`)
	// clauseJoin splits "fix the build and deploy nanite" or "check the
	// logs, then restart tether" into clauses.
	clauseJoin = regexp.MustCompile(`(?i)\s*(?:,?\s+and then\s+|,?\s+then\s+|,\s+and\s+|\s+and\s+|;\s*)`)
	// imperative is a clause that starts with a command verb, after an
	// optional "please", "can you" or "also".
	imperative = regexp.MustCompile(`(?i)^(?:please\s+|(?:can|could|would) you\s+|also\s+)*(fix|deploy|restart|check|run|ship|update|upgrade|merge|research|investigate|look into|find|compare|review|draft|write|summari[sz]e|analy[sz]e|ask|tell|file|create|open|test|build|release|tag|clean up|add|remove|rename|migrate|install|set up|configure|rebase|revert|roll back)\b`)
)

// Screen reads a message with rules only. It finds list items, several
// questions (URLs are not questions), commands joined by "and" or "then",
// joiners that start a second request, and task verbs.
func Screen(msg string) Screening {
	var s Screening
	if items := listLine.FindAllStringSubmatch(msg, -1); len(items) >= 2 {
		s.MultiPart = true
		for _, m := range items {
			s.Parts = append(s.Parts, strings.TrimSpace(m[1]))
		}
		s.Reasons = append(s.Reasons, fmt.Sprintf("%d list items", len(items)))
	}
	plain := urls.ReplaceAllString(msg, "URL")
	if qs := sentence.FindAllString(plain, -1); len(qs) >= 2 {
		s.MultiPart = true
		if len(s.Parts) == 0 {
			for _, q := range qs {
				s.Parts = append(s.Parts, strings.TrimSpace(q))
			}
		}
		s.Reasons = append(s.Reasons, fmt.Sprintf("%d questions", len(qs)))
	}
	if len(s.Parts) == 0 && !strings.Contains(strings.TrimSpace(plain), "\n") {
		var commands []string
		for _, c := range clauseJoin.Split(strings.TrimRight(strings.TrimSpace(msg), ".!"), -1) {
			if c = strings.TrimSpace(c); imperative.MatchString(c) {
				commands = append(commands, c)
			}
		}
		if len(commands) >= 2 {
			s.MultiPart = true
			s.Parts = commands
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d commands", len(commands)))
		}
	}
	if alsoJoiner.MatchString(msg) {
		s.MultiPart = true
		s.Reasons = append(s.Reasons, "a joiner starts another request")
	}
	if v := taskVerbs.FindString(msg); v != "" {
		s.Task = true
		s.Reasons = append(s.Reasons, fmt.Sprintf("task verb %q", strings.ToLower(v)))
	}
	return s
}

var (
	delegateVerbs = regexp.MustCompile(`\b(ask|have|tell) (the )?\w+ (agent|seat|team)\b|\bfile (a |the )?task|\bcreate (a )?tasks?\b|\bcoordinate with\b`)
	researchVerbs = regexp.MustCompile(`\b(research|investigate|look into|find out|dig into|survey|track down|benchmark)\b`)
	analysisVerbs = regexp.MustCompile(`\b(compare|analy[sz]e|review|audit|figure out|summari[sz]e)\b`)
)

// guessKind is the rules' guess at a part's kind.
func guessKind(part string) WorkKind {
	lower := strings.ToLower(part)
	switch {
	case delegateVerbs.MatchString(lower):
		return KindDelegate
	case researchVerbs.MatchString(lower):
		return KindResearch
	case analysisVerbs.MatchString(lower):
		return KindAnalysis
	}
	return KindAnswer
}

// Rules is the cheap decomposer: it splits a list or several questions
// into parts and guesses each part's kind from its verbs. Its confidence
// is low; it is the fallback when no model is configured or the model
// fails.
type Rules struct{}

// Decompose implements Decomposer.
func (Rules) Decompose(_ context.Context, req Request) (Plan, error) {
	s := Screen(req.Message)
	p := Plan{Message: req.Message, Mode: req.Mode, Source: "rules", Why: strings.Join(s.Reasons, "; ")}
	if !s.MultiPart || len(s.Parts) < 2 {
		kind := KindAnswer
		if s.Task {
			kind = guessKind(req.Message)
		}
		p.Items = []PlanItem{{ID: "1", Kind: kind, Goal: strings.TrimSpace(req.Message)}}
		p.Confidence = 0.6
		if kind == KindAnswer {
			p.Confidence = 0.8
		}
		return p, nil
	}
	for i, part := range s.Parts {
		p.Items = append(p.Items, PlanItem{ID: fmt.Sprint(i + 1), Kind: guessKind(part), Goal: part})
	}
	p.Confidence = 0.4
	return p, nil
}

// Complete is one model call: a system prompt and a user message in, the
// model's text out. Hosts adapt their provider to it.
type Complete func(ctx context.Context, system, user string) (string, error)

// Model decomposes with a small model. It asks for JSON, validates it,
// and asks once more with the error when the first answer does not
// validate.
type Model struct {
	Complete Complete
	// Instructions are added to the system prompt: the host's seats,
	// variants and anything else the planner should know.
	Instructions string
}

// Decompose implements Decomposer.
func (m Model) Decompose(ctx context.Context, req Request) (Plan, error) {
	if m.Complete == nil {
		return Plan{}, errors.New("desk: model decomposer has no Complete")
	}
	system := PlannerPrompt(m.Instructions)
	user := plannerInput(req)
	text, err := m.Complete(ctx, system, user)
	if err != nil {
		return Plan{}, err
	}
	p, perr := parsePlan(text, req)
	if perr == nil {
		return p, nil
	}
	retry := user + "\n\nYour previous answer was not a valid plan: " + perr.Error() + "\nAnswer again with only the JSON object."
	text, err = m.Complete(ctx, system, retry)
	if err != nil {
		return Plan{}, err
	}
	return parsePlan(text, req)
}

// PlannerPrompt is the system prompt the Model decomposer uses.
func PlannerPrompt(extra string) string {
	var b strings.Builder
	b.WriteString(`You are the front desk for an owner's personal agent. You read one message from the owner and split it into the work it asks for. You do not do the work and you do not answer the owner.

Answer with one JSON object and nothing else:
{"topic": "<id of an open topic this continues, or empty>",
 "topic_title": "<a short title for a new topic, when topic is empty>",
 "items": [{"id": "1", "kind": "<kind>", "goal": "<what to do, standing on its own>",
            "expect": "<what counts as done>", "depends_on": ["<ids>"], "target": "<variant or seat, if the owner named one>",
            "delivery": "<now|merge|hold|digest|file, or empty>"}],
 "confidence": <0 to 1, how sure you are of this split>,
 "why": "<one sentence>"}

Kinds:
- answer: the chat agent answers in the conversation. A message that only needs an answer is one answer item.
- research: find something out. Several research items run in parallel.
- analysis: reason over material already at hand.
- council: get several perspectives and synthesize them.
- delegate: a change to a system another seat owns; the goal says what to change and the expect says the acceptance criteria.
- question: something only the owner can decide before other items can run; other items depend on it.

Rules:
- Split only what the owner actually asked for. Do not invent work.
- Each goal must make sense without the original message.
- Use depends_on only when an item needs another's result.
- Leave delivery empty unless the owner said how they want it ("tell me when", "no rush", "just log it").
`)
	if strings.TrimSpace(extra) != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(extra))
		b.WriteString("\n")
	}
	return b.String()
}

func plannerInput(req Request) string {
	var b strings.Builder
	if req.Mode != "" {
		fmt.Fprintf(&b, "Mode: %s\n", req.Mode)
	}
	if len(req.Topics) > 0 {
		b.WriteString("Open topics:\n")
		for _, t := range req.Topics {
			fmt.Fprintf(&b, "- %s: %s\n", t.ID, t.Summary())
		}
	}
	b.WriteString("Message:\n")
	b.WriteString(req.Message)
	return b.String()
}

var fence = regexp.MustCompile("(?s)^\\s*```(?:json)?\\s*(.*?)\\s*```\\s*$")

func parsePlan(text string, req Request) (Plan, error) {
	text = strings.TrimSpace(text)
	if m := fence.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var p Plan
	dec := json.NewDecoder(strings.NewReader(text))
	if err := dec.Decode(&p); err != nil {
		return Plan{}, fmt.Errorf("not a JSON plan: %w", err)
	}
	p.Message = req.Message
	p.Mode = req.Mode
	p.Source = "model"
	if p.Topic != "" {
		known := false
		for _, t := range req.Topics {
			if t.ID == p.Topic {
				known = true
				break
			}
		}
		if !known {
			p.Topic = ""
		}
	}
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Disposition is what the host should do with a message after intake.
type Disposition string

const (
	// Chat sends the message to the chat agent as usual.
	Chat Disposition = "chat"
	// Suggest shows the plan card and waits for the owner. The message
	// still goes to the chat agent, which can acknowledge it.
	Suggest Disposition = "suggest"
	// Dispatch runs the plan.
	Dispatch Disposition = "dispatch"
)

// Intake is the front desk's first step: screen the message, plan it when
// it is worth it, and decide what to do with the plan.
type Intake struct {
	Mode IntakeMode
	// Planner plans messages the screen passes. Nil uses Rules.
	Planner Decomposer
	// AutoConfidence is the confidence an IntakeAuto plan needs to run
	// without asking (default 0.8). Below it, the plan is suggested.
	AutoConfidence float64
}

// Result is intake's output.
type Result struct {
	Screening   Screening   `json:"screening"`
	Plan        Plan        `json:"plan"`
	Planned     bool        `json:"planned"` // the planner ran
	Disposition Disposition `json:"disposition"`
	Err         string      `json:"error,omitempty"` // the planner failed; Plan is the rules' fallback
}

// Read screens and, when worth it, plans one message. A failed model plan
// falls back to the rules' plan and is reported in Err; intake never
// blocks the message.
func (in Intake) Read(ctx context.Context, req Request) (Result, error) {
	mode := in.Mode
	if mode == "" {
		mode = IntakeOff
	}
	if !mode.valid() {
		return Result{}, fmt.Errorf("desk: unknown intake mode %q", in.Mode)
	}
	r := Result{Screening: Screen(req.Message), Disposition: Chat}
	if mode == IntakeOff || !r.Screening.Worth() {
		r.Plan = Plan{Message: req.Message, Mode: req.Mode, Source: "rules", Confidence: 1,
			Items: []PlanItem{{ID: "1", Kind: KindAnswer, Goal: strings.TrimSpace(req.Message)}}}
		return r, nil
	}
	planner := in.Planner
	if planner == nil {
		planner = Rules{}
	}
	p, err := planner.Decompose(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		r.Err = err.Error()
		p, _ = Rules{}.Decompose(ctx, req)
	}
	r.Plan, r.Planned = p, true
	if p.Trivial() {
		return r, nil
	}
	switch mode {
	case IntakeSuggest:
		r.Disposition = Suggest
	case IntakeAuto:
		threshold := in.AutoConfidence
		if threshold == 0 {
			threshold = 0.8
		}
		if p.Confidence >= threshold && r.Err == "" {
			r.Disposition = Dispatch
		} else {
			r.Disposition = Suggest
		}
	}
	return r, nil
}

// Correction is the owner's answer to a suggested plan: accepted as is,
// edited, or declined. Hosts keep these to measure the planner and to
// decide when IntakeAuto is safe.
type Correction struct {
	Proposed Plan  `json:"proposed"`
	Final    *Plan `json:"final,omitempty"` // nil when declined
	Edited   bool  `json:"edited"`
}

// Accepted reports whether the owner ran the plan, edited or not.
func (c Correction) Accepted() bool { return c.Final != nil }
