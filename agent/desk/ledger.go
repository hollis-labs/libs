package desk

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// TopicID identifies a topic in a Ledger.
type TopicID string

// TopicStatus is where a topic stands.
type TopicStatus string

const (
	// TopicOpen has work in flight or results the owner has not seen.
	TopicOpen TopicStatus = "open"
	// TopicClosed is finished; it stays in the ledger for reference.
	TopicClosed TopicStatus = "closed"
)

// WorkState is where one piece of work stands.
type WorkState string

const (
	WorkPlanned  WorkState = "planned"  // in a plan, not started
	WorkRunning  WorkState = "running"  // started, no result yet
	WorkDone     WorkState = "done"     // finished with a result
	WorkFailed   WorkState = "failed"   // finished without a usable result
	WorkCanceled WorkState = "canceled" // stopped before it finished
)

// Finished reports whether the state is terminal.
func (s WorkState) Finished() bool {
	return s == WorkDone || s == WorkFailed || s == WorkCanceled
}

func (s WorkState) valid() bool {
	switch s {
	case WorkPlanned, WorkRunning, WorkDone, WorkFailed, WorkCanceled:
		return true
	}
	return false
}

// Ref points at something outside the ledger that belongs to a topic: a
// conversation, a background job, a mail thread, a task, a seat.
type Ref struct {
	Kind string `json:"kind"` // for example "conversation", "job", "thread", "task", "seat"
	ID   string `json:"id"`
}

func (r Ref) String() string { return r.Kind + ":" + r.ID }

// Work is one piece of work attached to a topic.
type Work struct {
	ID       string    `json:"id"`
	Kind     WorkKind  `json:"kind"`
	Summary  string    `json:"summary"`
	State    WorkState `json:"state"`
	Ref      Ref       `json:"ref,omitzero"` // what runs it, for example the job
	Result   string    `json:"result,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
}

// Question is a question to the owner, open until answered or withdrawn.
type Question struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Asked     time.Time `json:"asked"`
	Answered  time.Time `json:"answered,omitzero"`
	Answer    string    `json:"answer,omitempty"`
	Withdrawn time.Time `json:"withdrawn,omitzero"`
}

// Open reports whether the question still waits for the owner.
func (q Question) Open() bool { return q.Answered.IsZero() && q.Withdrawn.IsZero() }

// Topic is one open line of the owner's business: the work in flight on
// it, what came back, questions waiting on the owner, and related refs.
type Topic struct {
	ID        TopicID     `json:"id"`
	Title     string      `json:"title"`
	Status    TopicStatus `json:"status"`
	Mode      string      `json:"mode,omitempty"`
	Created   time.Time   `json:"created"`
	Updated   time.Time   `json:"updated"`
	Work      []Work      `json:"work"`
	Questions []Question  `json:"questions"`
	Refs      []Ref       `json:"refs"`
}

// InFlight is the work on the topic that has not finished.
func (t Topic) InFlight() []Work {
	var out []Work
	for _, w := range t.Work {
		if !w.State.Finished() {
			out = append(out, w)
		}
	}
	return out
}

// OpenQuestions are the questions still waiting on the owner.
func (t Topic) OpenQuestions() []Question {
	var out []Question
	for _, q := range t.Questions {
		if q.Open() {
			out = append(out, q)
		}
	}
	return out
}

// Summary is a short plain-text view of the topic for a prompt or a card.
func (t Topic) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)", t.Title, t.ID)
	if n := len(t.InFlight()); n > 0 {
		fmt.Fprintf(&b, ", %d in flight", n)
	}
	if n := len(t.OpenQuestions()); n > 0 {
		fmt.Fprintf(&b, ", %d question(s) for the owner", n)
	}
	return b.String()
}

func (t Topic) clone() Topic {
	t.Work = slices.Clone(t.Work)
	t.Questions = slices.Clone(t.Questions)
	t.Refs = slices.Clone(t.Refs)
	return t
}

// Errors returned by a Ledger.
var (
	ErrNoTopic    = errors.New("desk: no such topic")
	ErrNoWork     = errors.New("desk: no such work on the topic")
	ErrNoQuestion = errors.New("desk: no such question on the topic")
	ErrClosed     = errors.New("desk: topic is closed")
	ErrDuplicate  = errors.New("desk: id already used")
)

// Ledger keeps the open topics. It is safe for concurrent use. Hosts that
// need it to survive a restart persist it with MarshalJSON and restore it
// with UnmarshalJSON, or keep their own store and rebuild it.
type Ledger struct {
	mu     sync.Mutex
	now    func() time.Time
	topics map[TopicID]*Topic
	byRef  map[Ref]TopicID
	seq    int
}

// NewLedger returns an empty ledger. A nil clock uses time.Now.
func NewLedger(now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{now: now, topics: map[TopicID]*Topic{}, byRef: map[Ref]TopicID{}}
}

// Open starts a topic and returns it. An empty id gets a generated one.
func (l *Ledger) Open(id TopicID, title, mode string, refs ...Ref) (Topic, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id == "" {
		l.seq++
		id = TopicID(fmt.Sprintf("t%d", l.seq))
		for l.topics[id] != nil {
			l.seq++
			id = TopicID(fmt.Sprintf("t%d", l.seq))
		}
	}
	if l.topics[id] != nil {
		return Topic{}, fmt.Errorf("%w: topic %s", ErrDuplicate, id)
	}
	now := l.now()
	t := &Topic{ID: id, Title: title, Status: TopicOpen, Mode: mode, Created: now, Updated: now, Work: []Work{}, Questions: []Question{}, Refs: []Ref{}}
	l.topics[id] = t
	for _, r := range refs {
		l.linkLocked(t, r)
	}
	return t.clone(), nil
}

// Get returns a copy of the topic.
func (l *Ledger) Get(id TopicID) (Topic, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.topics[id]
	if !ok {
		return Topic{}, fmt.Errorf("%w: %s", ErrNoTopic, id)
	}
	return t.clone(), nil
}

// OpenTopics returns the open topics, most recently updated first.
func (l *Ledger) OpenTopics() []Topic {
	return l.List(func(t Topic) bool { return t.Status == TopicOpen })
}

// List returns copies of the topics keep accepts (all when keep is nil),
// most recently updated first.
func (l *Ledger) List(keep func(Topic) bool) []Topic {
	l.mu.Lock()
	out := make([]Topic, 0, len(l.topics))
	for _, t := range l.topics {
		if keep == nil || keep(*t) {
			out = append(out, t.clone())
		}
	}
	l.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// FindWork returns the topic and the work a ref started, for recording a
// finished job: the work whose Ref it is.
func (l *Ledger) FindWork(r Ref) (Topic, Work, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id, ok := l.byRef[r]
	if !ok {
		return Topic{}, Work{}, false
	}
	t := l.topics[id]
	for _, w := range t.Work {
		if w.Ref == r {
			return t.clone(), w, true
		}
	}
	return Topic{}, Work{}, false
}

// ProgressRef moves the work a ref started (see FindWork).
func (l *Ledger) ProgressRef(r Ref, state WorkState, result string) (Topic, Work, error) {
	t, w, ok := l.FindWork(r)
	if !ok {
		return Topic{}, Work{}, fmt.Errorf("%w: no work for %s", ErrNoWork, r)
	}
	if err := l.Progress(t.ID, w.ID, state, result); err != nil {
		return Topic{}, Work{}, err
	}
	t, err := l.Get(t.ID)
	if err != nil {
		return Topic{}, Work{}, err
	}
	for _, x := range t.Work {
		if x.ID == w.ID {
			w = x
		}
	}
	return t, w, nil
}

// Find returns the topic a ref is linked to, for routing a result back to
// the topic that started the work.
func (l *Ledger) Find(r Ref) (Topic, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id, ok := l.byRef[r]
	if !ok {
		return Topic{}, false
	}
	return l.topics[id].clone(), true
}

// Link relates a ref to a topic. A ref belongs to one topic; linking it
// again moves it.
func (l *Ledger) Link(id TopicID, r Ref) error {
	return l.update(id, false, func(t *Topic) error {
		l.linkLocked(t, r)
		return nil
	})
}

func (l *Ledger) linkLocked(t *Topic, r Ref) {
	if r.Kind == "" || r.ID == "" {
		return
	}
	if old, ok := l.byRef[r]; ok && old != t.ID {
		if prev := l.topics[old]; prev != nil {
			prev.Refs = slices.DeleteFunc(prev.Refs, func(x Ref) bool { return x == r })
		}
	}
	l.byRef[r] = t.ID
	if !slices.Contains(t.Refs, r) {
		t.Refs = append(t.Refs, r)
	}
}

// Attach adds work to a topic. A work's Ref is linked to the topic too.
func (l *Ledger) Attach(id TopicID, w Work) error {
	if w.ID == "" {
		return errors.New("desk: work needs an id")
	}
	if w.State == "" {
		w.State = WorkPlanned
	}
	if !w.State.valid() {
		return fmt.Errorf("desk: unknown work state %q", w.State)
	}
	return l.update(id, true, func(t *Topic) error {
		if slices.ContainsFunc(t.Work, func(x Work) bool { return x.ID == w.ID }) {
			return fmt.Errorf("%w: work %s", ErrDuplicate, w.ID)
		}
		now := l.now()
		if w.State != WorkPlanned && w.Started.IsZero() {
			w.Started = now
		}
		if w.State.Finished() && w.Finished.IsZero() {
			w.Finished = now
		}
		t.Work = append(t.Work, w)
		l.linkLocked(t, w.Ref)
		return nil
	})
}

// Progress moves work to a new state, with its result when it finished.
// A finished work does not move again.
func (l *Ledger) Progress(id TopicID, workID string, state WorkState, result string) error {
	if !state.valid() {
		return fmt.Errorf("desk: unknown work state %q", state)
	}
	return l.update(id, false, func(t *Topic) error {
		i := slices.IndexFunc(t.Work, func(x Work) bool { return x.ID == workID })
		if i < 0 {
			return fmt.Errorf("%w: %s", ErrNoWork, workID)
		}
		w := &t.Work[i]
		if w.State.Finished() {
			return fmt.Errorf("desk: work %s already %s", workID, w.State)
		}
		now := l.now()
		if state != WorkPlanned && w.Started.IsZero() {
			w.Started = now
		}
		w.State = state
		if state.Finished() {
			w.Finished = now
			w.Result = result
		}
		return nil
	})
}

// Ask records a question to the owner. An empty question id gets one.
func (l *Ledger) Ask(id TopicID, questionID, text string) (Question, error) {
	var q Question
	err := l.update(id, true, func(t *Topic) error {
		if questionID == "" {
			questionID = fmt.Sprintf("q%d", len(t.Questions)+1)
			for slices.ContainsFunc(t.Questions, func(x Question) bool { return x.ID == questionID }) {
				questionID += "'"
			}
		}
		if slices.ContainsFunc(t.Questions, func(x Question) bool { return x.ID == questionID }) {
			return fmt.Errorf("%w: question %s", ErrDuplicate, questionID)
		}
		q = Question{ID: questionID, Text: text, Asked: l.now()}
		t.Questions = append(t.Questions, q)
		return nil
	})
	return q, err
}

// Answer records the owner's answer and closes the question.
func (l *Ledger) Answer(id TopicID, questionID, answer string) error {
	return l.update(id, false, func(t *Topic) error {
		i := slices.IndexFunc(t.Questions, func(x Question) bool { return x.ID == questionID })
		if i < 0 {
			return fmt.Errorf("%w: %s", ErrNoQuestion, questionID)
		}
		if !t.Questions[i].Open() {
			return fmt.Errorf("desk: question %s is not open", questionID)
		}
		t.Questions[i].Answered = l.now()
		t.Questions[i].Answer = answer
		return nil
	})
}

// Withdraw closes a question without an answer: it no longer matters.
func (l *Ledger) Withdraw(id TopicID, questionID string) error {
	return l.update(id, false, func(t *Topic) error {
		i := slices.IndexFunc(t.Questions, func(x Question) bool { return x.ID == questionID })
		if i < 0 {
			return fmt.Errorf("%w: %s", ErrNoQuestion, questionID)
		}
		if !t.Questions[i].Open() {
			return fmt.Errorf("desk: question %s is not open", questionID)
		}
		t.Questions[i].Withdrawn = l.now()
		return nil
	})
}

// HasOpenQuestion reports whether the topic waits on the owner.
func (l *Ledger) HasOpenQuestion(id TopicID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.topics[id]
	return t != nil && slices.ContainsFunc(t.Questions, Question.Open)
}

// Close finishes a topic and withdraws its open questions. Running work is
// left as it is; the host cancels it if it should stop.
func (l *Ledger) Close(id TopicID) error {
	return l.update(id, false, func(t *Topic) error {
		t.Status = TopicClosed
		now := l.now()
		for i := range t.Questions {
			if t.Questions[i].Open() {
				t.Questions[i].Withdrawn = now
			}
		}
		return nil
	})
}

// Reopen opens a closed topic again.
func (l *Ledger) Reopen(id TopicID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.topics[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoTopic, id)
	}
	t.Status = TopicOpen
	t.Updated = l.now()
	return nil
}

// update applies fn to a topic under the lock and stamps it. requireOpen
// refuses a closed topic (for new work and questions).
func (l *Ledger) update(id TopicID, requireOpen bool, fn func(*Topic) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.topics[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoTopic, id)
	}
	if requireOpen && t.Status == TopicClosed {
		return fmt.Errorf("%w: %s", ErrClosed, id)
	}
	if err := fn(t); err != nil {
		return err
	}
	t.Updated = l.now()
	return nil
}

type ledgerJSON struct {
	Topics []Topic `json:"topics"`
	Seq    int     `json:"seq"`
}

// MarshalJSON writes every topic, for a host to persist.
func (l *Ledger) MarshalJSON() ([]byte, error) {
	topics := l.List(nil)
	l.mu.Lock()
	seq := l.seq
	l.mu.Unlock()
	return json.Marshal(ledgerJSON{Topics: topics, Seq: seq})
}

// UnmarshalJSON replaces the ledger's topics with saved ones, or leaves the
// ledger unchanged when the data is invalid. The clock is kept (time.Now on
// a zero Ledger).
func (l *Ledger) UnmarshalJSON(data []byte) error {
	var in ledgerJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	topics := map[TopicID]*Topic{}
	byRef := map[Ref]TopicID{}
	for i := range in.Topics {
		t := in.Topics[i].clone()
		if t.ID == "" || topics[t.ID] != nil {
			return fmt.Errorf("desk: saved ledger has an empty or repeated topic id %q", t.ID)
		}
		if t.Work == nil {
			t.Work = []Work{}
		}
		if t.Questions == nil {
			t.Questions = []Question{}
		}
		if t.Refs == nil {
			t.Refs = []Ref{}
		}
		topics[t.ID] = &t
		for _, r := range t.Refs {
			byRef[r] = t.ID
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.now == nil {
		l.now = time.Now
	}
	l.topics, l.byRef, l.seq = topics, byRef, in.Seq
	return nil
}
