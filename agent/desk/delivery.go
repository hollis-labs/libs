package desk

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// ItemKind is what an outbox item is.
type ItemKind string

const (
	ItemResult   ItemKind = "result"   // work came back
	ItemFailure  ItemKind = "failure"  // work came back without a usable result
	ItemQuestion ItemKind = "question" // the owner must answer
	ItemProgress ItemKind = "progress" // work is moving; rarely worth a message
	ItemNotice   ItemKind = "notice"   // anything else the agent wants to say unprompted
)

func (k ItemKind) valid() bool {
	switch k {
	case ItemResult, ItemFailure, ItemQuestion, ItemProgress, ItemNotice:
		return true
	}
	return false
}

// Urgency orders items. The zero value is Normal.
type Urgency int

const (
	Low      Urgency = -1
	Normal   Urgency = 0
	High     Urgency = 1
	Critical Urgency = 2
)

func (u Urgency) String() string {
	switch u {
	case Low:
		return "low"
	case Normal:
		return "normal"
	case High:
		return "high"
	case Critical:
		return "critical"
	}
	return fmt.Sprintf("urgency(%d)", int(u))
}

// Action is what the delivery editor does with an item.
type Action string

const (
	// Now releases the item at the next chance, with anything else due.
	Now Action = "now"
	// Merge waits a short window for more on the same topic, then releases
	// them together.
	Merge Action = "merge"
	// Hold waits while the topic has a question open to the owner, so the
	// owner is not handed new material in the middle of answering.
	Hold Action = "hold"
	// Digest collects the item into a periodic summary.
	Digest Action = "digest"
	// File records the item without a message (a done-for-you log).
	File Action = "file"
)

// Actions lists the known actions.
func Actions() []Action { return []Action{Now, Merge, Hold, Digest, File} }

func (a Action) valid() bool { return slices.Contains(Actions(), a) }

// Item is one thing that could reach the owner.
type Item struct {
	ID      string    `json:"id"`
	Topic   TopicID   `json:"topic,omitempty"`
	Kind    ItemKind  `json:"kind"`
	Urgency Urgency   `json:"urgency,omitempty"`
	Source  string    `json:"source,omitempty"` // who produced it: an app, module, variant or seat
	Title   string    `json:"title,omitempty"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	// Prefer is the producer's own choice, used unless the policy's
	// invariants or an earlier rule decide otherwise.
	Prefer Action `json:"prefer,omitempty"`
}

// State is what the editor knows about the owner when it decides.
type State struct {
	Mode string // the current deliberate mode, if any
	// Busy is true while the owner is typing or a turn is running. Only
	// critical items and owner questions interrupt a busy owner.
	Busy bool
}

// Match selects items for a rule. Empty fields match anything.
type Match struct {
	Kinds   []ItemKind `json:"kinds,omitempty"`
	Sources []string   `json:"sources,omitempty"` // exact, or a prefix ending in "*"
	Modes   []string   `json:"modes,omitempty"`
	// MinUrgency and MaxUrgency bound the item's urgency when set.
	MinUrgency *Urgency `json:"min_urgency,omitempty"`
	MaxUrgency *Urgency `json:"max_urgency,omitempty"`
	// QuestionOpen, when set, matches on whether the item's topic has a
	// question open to the owner.
	QuestionOpen *bool `json:"question_open,omitempty"`
	// Preferred, when set, matches items whose producer asked for this action.
	Preferred Action `json:"preferred,omitempty"`
}

// Rule maps matching items to an action. The first rule that matches wins.
type Rule struct {
	Name string `json:"name"`
	When Match  `json:"when"`
	Do   Action `json:"do"`
}

// Policy is a delivery editor's configuration.
type Policy struct {
	Rules       []Rule        `json:"rules"`
	Default     Action        `json:"default,omitempty"`      // when no rule matches
	MergeWindow time.Duration `json:"merge_window,omitempty"` // how long Merge waits
	HoldMax     time.Duration `json:"hold_max,omitempty"`     // the longest Hold waits for an answer
	DigestEvery time.Duration `json:"digest_every,omitempty"` // how often a digest goes out
	DigestMax   int           `json:"digest_max,omitempty"`   // a digest goes out early at this many items
	BusyMax     time.Duration `json:"busy_max,omitempty"`     // the longest a busy owner defers due items (0: until not busy)
	// Quiet stops digest items riding along: by default, when a release
	// goes out anyway, pending digest items join it, so the owner gets one
	// message instead of a separate digest later.
	Quiet bool `json:"quiet,omitempty"`
}

// Ptr returns a pointer to v, for Match's optional fields.
func Ptr[T any](v T) *T { return &v }

// DefaultPolicy is the shipped policy. Apps and modules layer their own
// over it with Layer.
func DefaultPolicy() Policy {
	return Policy{
		Rules: []Rule{
			// In a focus mode only urgent things and questions get through;
			// the rest waits for the digest.
			{Name: "focus", When: Match{Modes: []string{"focus"}, MaxUrgency: Ptr(Normal), Kinds: []ItemKind{ItemResult, ItemFailure, ItemNotice}}, Do: Digest},
			{Name: "urgent", When: Match{MinUrgency: Ptr(High)}, Do: Now},
			{Name: "preferred-now", When: Match{Preferred: Now}, Do: Now},
			{Name: "waiting-on-owner", When: Match{QuestionOpen: Ptr(true), Kinds: []ItemKind{ItemResult, ItemFailure, ItemNotice}}, Do: Hold},
			{Name: "preferred-digest", When: Match{Preferred: Digest}, Do: Digest},
			{Name: "preferred-file", When: Match{Preferred: File}, Do: File},
			{Name: "results", When: Match{Kinds: []ItemKind{ItemResult, ItemFailure}}, Do: Merge},
			{Name: "progress", When: Match{Kinds: []ItemKind{ItemProgress}}, Do: File},
			{Name: "notices", When: Match{Kinds: []ItemKind{ItemNotice}, MaxUrgency: Ptr(Low)}, Do: Digest},
		},
		Default:     Merge,
		MergeWindow: 20 * time.Second,
		HoldMax:     30 * time.Minute,
		DigestEvery: 30 * time.Minute,
		DigestMax:   10,
	}
}

// Layer puts policies over a base: each later layer's rules go in front
// of the earlier ones (so a later layer wins), and its non-zero settings
// replace the earlier ones. Use it as Layer(DefaultPolicy(), app, module)
// in the order the host wants to win.
func Layer(base Policy, layers ...Policy) Policy {
	out := base
	out.Rules = slices.Clone(base.Rules)
	for _, l := range layers {
		out.Rules = append(slices.Clone(l.Rules), out.Rules...)
		if l.Default != "" {
			out.Default = l.Default
		}
		if l.MergeWindow != 0 {
			out.MergeWindow = l.MergeWindow
		}
		if l.HoldMax != 0 {
			out.HoldMax = l.HoldMax
		}
		if l.DigestEvery != 0 {
			out.DigestEvery = l.DigestEvery
		}
		if l.DigestMax != 0 {
			out.DigestMax = l.DigestMax
		}
		if l.BusyMax != 0 {
			out.BusyMax = l.BusyMax
		}
		if l.Quiet {
			out.Quiet = true
		}
	}
	return out
}

// Validate checks the rules' actions and the settings.
func (p Policy) Validate() error {
	for i, r := range p.Rules {
		if !r.Do.valid() {
			return fmt.Errorf("desk: rule %d (%s) has unknown action %q", i, r.Name, r.Do)
		}
		for _, k := range r.When.Kinds {
			if !k.valid() {
				return fmt.Errorf("desk: rule %d (%s) matches unknown kind %q", i, r.Name, k)
			}
		}
		if r.When.Preferred != "" && !r.When.Preferred.valid() {
			return fmt.Errorf("desk: rule %d (%s) matches unknown preferred action %q", i, r.Name, r.When.Preferred)
		}
	}
	if p.Default != "" && !p.Default.valid() {
		return fmt.Errorf("desk: unknown default action %q", p.Default)
	}
	if p.MergeWindow < 0 || p.HoldMax < 0 || p.DigestEvery < 0 || p.DigestMax < 0 || p.BusyMax < 0 {
		return fmt.Errorf("desk: policy durations and counts must not be negative")
	}
	return nil
}

func (m Match) matches(it Item, st State, questionOpen bool) bool {
	if len(m.Kinds) > 0 && !slices.Contains(m.Kinds, it.Kind) {
		return false
	}
	if len(m.Modes) > 0 && !slices.Contains(m.Modes, st.Mode) {
		return false
	}
	if len(m.Sources) > 0 && !slices.ContainsFunc(m.Sources, func(s string) bool {
		if prefix, ok := strings.CutSuffix(s, "*"); ok {
			return strings.HasPrefix(it.Source, prefix)
		}
		return s == it.Source
	}) {
		return false
	}
	if m.MinUrgency != nil && it.Urgency < *m.MinUrgency {
		return false
	}
	if m.MaxUrgency != nil && it.Urgency > *m.MaxUrgency {
		return false
	}
	if m.QuestionOpen != nil && *m.QuestionOpen != questionOpen {
		return false
	}
	if m.Preferred != "" && m.Preferred != it.Prefer {
		return false
	}
	return true
}

// Decision is what the editor did with an item and why.
type Decision struct {
	Action Action `json:"action"`
	Rule   string `json:"rule"` // the rule that decided, or "invariant:..." / "default"
}

// Decide applies the policy to one item. Two invariants come first and no
// rule can change them: a critical item and a question to the owner are
// always released now.
func (p Policy) Decide(it Item, st State, questionOpen bool) Decision {
	switch {
	case it.Urgency >= Critical:
		return Decision{Action: Now, Rule: "invariant:critical"}
	case it.Kind == ItemQuestion:
		return Decision{Action: Now, Rule: "invariant:question"}
	}
	for _, r := range p.Rules {
		if r.When.matches(it, st, questionOpen) {
			return Decision{Action: r.Do, Rule: r.Name}
		}
	}
	if p.Default != "" {
		return Decision{Action: p.Default, Rule: "default"}
	}
	return Decision{Action: Merge, Rule: "default"}
}

// Message is one release to the owner: every item due at that moment,
// grouped by topic.
type Message struct {
	At     time.Time `json:"at"`
	Items  []Queued  `json:"items"`
	Topics []TopicID `json:"topics"`
	Text   string    `json:"text"`
}

// Queued is an item with the decision that placed it.
type Queued struct {
	Item     Item      `json:"item"`
	Decision Decision  `json:"decision"`
	Queued   time.Time `json:"queued"`
}

// Composer renders a release. The default lists items under their topics.
type Composer func(items []Queued, titles map[TopicID]string) string

// Editor queues items and releases them by policy. It does not deliver:
// the host calls Due when an item is submitted, when the owner's state
// changes, when a question is answered, and at the time Next returns, and
// sends what Due returns. It is safe for concurrent use.
type Editor struct {
	mu      sync.Mutex
	policy  Policy
	ledger  *Ledger
	now     func() time.Time
	compose Composer
	pending []Queued
	filed   []Queued
	busyAt  time.Time // when the owner became busy with items due
}

// EditorOption configures an Editor.
type EditorOption func(*Editor)

// WithLedger lets Hold and topic titles use the ledger.
func WithLedger(l *Ledger) EditorOption { return func(e *Editor) { e.ledger = l } }

// WithClock sets the editor's clock.
func WithClock(now func() time.Time) EditorOption { return func(e *Editor) { e.now = now } }

// WithComposer replaces the default rendering of a release.
func WithComposer(c Composer) EditorOption { return func(e *Editor) { e.compose = c } }

// NewEditor returns an editor for a validated policy.
func NewEditor(p Policy, opts ...EditorOption) (*Editor, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	e := &Editor{policy: p, now: time.Now, compose: DefaultComposer}
	for _, o := range opts {
		o(e)
	}
	return e, nil
}

// Policy returns the editor's policy.
func (e *Editor) Policy() Policy {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.policy
}

// SetPolicy replaces the policy for items submitted from now on (a mode
// change, for example). Items already queued keep their decision.
func (e *Editor) SetPolicy(p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policy = p
	return nil
}

func (e *Editor) questionOpen(topic TopicID) bool {
	return e.ledger != nil && topic != "" && e.ledger.HasOpenQuestion(topic)
}

// Submit decides an item and queues or files it. It returns the decision;
// call Due to collect what is ready.
func (e *Editor) Submit(it Item, st State) Decision {
	now := e.now()
	if it.At.IsZero() {
		it.At = now
	}
	open := e.questionOpen(it.Topic)
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.policy.Decide(it, st, open)
	q := Queued{Item: it, Decision: d, Queued: now}
	if d.Action == File {
		e.filed = append(e.filed, q)
		return d
	}
	e.pending = append(e.pending, q)
	return d
}

// due reports whether a queued item is ready at now. Items are released
// with anything else due on their topic, so a Merge item on a topic with a
// Now item goes out with it.
func (e *Editor) due(q Queued, now time.Time, digestDue bool) bool {
	switch q.Decision.Action {
	case Now:
		return true
	case Merge:
		return !now.Before(q.Queued.Add(e.policy.MergeWindow))
	case Hold:
		return !e.questionOpen(q.Item.Topic) || (e.policy.HoldMax > 0 && !now.Before(q.Queued.Add(e.policy.HoldMax)))
	case Digest:
		return digestDue
	}
	return false
}

func (e *Editor) digestDue(now time.Time) bool {
	var n int
	var oldest time.Time
	for _, q := range e.pending {
		if q.Decision.Action != Digest {
			continue
		}
		n++
		if oldest.IsZero() || q.Queued.Before(oldest) {
			oldest = q.Queued
		}
	}
	if n == 0 {
		return false
	}
	return (e.policy.DigestMax > 0 && n >= e.policy.DigestMax) || (e.policy.DigestEvery > 0 && !now.Before(oldest.Add(e.policy.DigestEvery)))
}

// interrupts reports whether a queued item may reach a busy owner.
func interrupts(q Queued) bool {
	return q.Item.Urgency >= Critical || q.Item.Kind == ItemQuestion
}

// Due removes and returns everything ready now as one message, or false
// when nothing is. While the owner is busy only items that interrupt
// (critical, owner questions) go out, unless BusyMax has passed.
func (e *Editor) Due(st State) (Message, bool) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	digest := e.digestDue(now)
	ready := map[int]bool{}
	topics := map[TopicID]bool{}
	for i, q := range e.pending {
		if e.due(q, now, digest) {
			ready[i] = true
			if q.Item.Topic != "" {
				topics[q.Item.Topic] = true
			}
		}
	}
	// Anything waiting to merge on a topic that is going out goes with it.
	for i, q := range e.pending {
		if !ready[i] && q.Decision.Action == Merge && topics[q.Item.Topic] && q.Item.Topic != "" {
			ready[i] = true
		}
	}
	if st.Busy && len(ready) > 0 {
		if e.busyAt.IsZero() {
			e.busyAt = now
		}
		waited := e.policy.BusyMax > 0 && !now.Before(e.busyAt.Add(e.policy.BusyMax))
		if !waited {
			for i := range ready {
				if !interrupts(e.pending[i]) {
					delete(ready, i)
				}
			}
		}
	}
	if !st.Busy {
		e.busyAt = time.Time{}
	}
	if len(ready) == 0 {
		return Message{}, false
	}
	// Digest items ride along with a release that goes out anyway (unless
	// the owner is busy and only an interruption is going out).
	if !e.policy.Quiet && !st.Busy {
		for i, q := range e.pending {
			if q.Decision.Action == Digest {
				ready[i] = true
			}
		}
	}
	var out, keep []Queued
	for i, q := range e.pending {
		if ready[i] {
			out = append(out, q)
		} else {
			keep = append(keep, q)
		}
	}
	e.pending = keep
	if len(e.pending) == 0 || !st.Busy {
		e.busyAt = time.Time{}
	}
	return e.message(now, out), true
}

// Flush removes and returns everything pending as one message, for an
// owner who asks what they missed.
func (e *Editor) Flush() (Message, bool) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pending) == 0 {
		return Message{}, false
	}
	out := e.pending
	e.pending = nil
	e.busyAt = time.Time{}
	return e.message(now, out), true
}

func (e *Editor) message(now time.Time, items []Queued) Message {
	// Group by topic, topics in order of their first item; items without
	// a topic last.
	var order []TopicID
	byTopic := map[TopicID][]Queued{}
	for _, q := range items {
		if _, ok := byTopic[q.Item.Topic]; !ok {
			order = append(order, q.Item.Topic)
		}
		byTopic[q.Item.Topic] = append(byTopic[q.Item.Topic], q)
	}
	slices.SortStableFunc(order, func(a, b TopicID) int {
		switch {
		case a == "" && b != "":
			return 1
		case a != "" && b == "":
			return -1
		}
		return 0
	})
	var grouped []Queued
	var topics []TopicID
	titles := map[TopicID]string{}
	for _, t := range order {
		grouped = append(grouped, byTopic[t]...)
		if t == "" {
			continue
		}
		topics = append(topics, t)
		if e.ledger != nil {
			if topic, err := e.ledger.Get(t); err == nil {
				titles[t] = topic.Title
			}
		}
	}
	return Message{At: now, Items: grouped, Topics: topics, Text: e.compose(grouped, titles)}
}

// Next returns when the next queued item becomes due by time, or false
// when none waits on the clock (only on the owner, an answer, or nothing).
// While the owner is busy, items that do not interrupt wait for BusyMax
// (or, without it, for the owner).
func (e *Editor) Next(st State) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var next time.Time
	busyUntil := time.Time{}
	if st.Busy && e.policy.BusyMax > 0 {
		busyUntil = e.now().Add(e.policy.BusyMax)
		if !e.busyAt.IsZero() {
			busyUntil = e.busyAt.Add(e.policy.BusyMax)
		}
	}
	consider := func(q Queued, t time.Time) {
		if st.Busy && !interrupts(q) {
			if busyUntil.IsZero() {
				return
			}
			if t.Before(busyUntil) {
				t = busyUntil
			}
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	var digestOldest *Queued
	for i, q := range e.pending {
		switch q.Decision.Action {
		case Now:
			consider(q, q.Queued)
		case Merge:
			consider(q, q.Queued.Add(e.policy.MergeWindow))
		case Hold:
			if e.policy.HoldMax > 0 {
				consider(q, q.Queued.Add(e.policy.HoldMax))
			}
		case Digest:
			if digestOldest == nil || q.Queued.Before(digestOldest.Queued) {
				digestOldest = &e.pending[i]
			}
		}
	}
	if digestOldest != nil && e.policy.DigestEvery > 0 {
		consider(*digestOldest, digestOldest.Queued.Add(e.policy.DigestEvery))
	}
	return next, !next.IsZero()
}

// Pending returns copies of the queued items.
func (e *Editor) Pending() []Queued {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.pending)
}

// Held counts the queued items by action, for a "3 held" indicator.
func (e *Editor) Held() map[Action]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[Action]int{}
	for _, q := range e.pending {
		out[q.Decision.Action]++
	}
	return out
}

// Filed returns the items filed without a message, oldest first.
func (e *Editor) Filed() []Queued {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.filed)
}

// TakeFiled removes and returns the filed items, for a host that keeps
// its own done-for-you log.
func (e *Editor) TakeFiled() []Queued {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.filed
	e.filed = nil
	return out
}

// DefaultComposer renders a release as plain text: one block per topic,
// one line per item, questions first within a topic. A single item is
// just its text.
func DefaultComposer(items []Queued, titles map[TopicID]string) string {
	if len(items) == 1 {
		return itemLine(items[0].Item, false)
	}
	var b strings.Builder
	var current TopicID
	first := true
	for _, q := range orderQuestionsFirst(items) {
		if first || q.Item.Topic != current {
			if !first {
				b.WriteString("\n")
			}
			current = q.Item.Topic
			switch {
			case current == "":
				b.WriteString("Other:\n")
			case titles[current] != "":
				b.WriteString(titles[current] + ":\n")
			default:
				b.WriteString(string(current) + ":\n")
			}
			first = false
		}
		b.WriteString("- " + itemLine(q.Item, true) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func orderQuestionsFirst(items []Queued) []Queued {
	out := slices.Clone(items)
	// Stable within each topic block: questions before the rest.
	slices.SortStableFunc(out, func(a, b Queued) int {
		if a.Item.Topic != b.Item.Topic {
			return 0
		}
		qa, qb := a.Item.Kind == ItemQuestion, b.Item.Kind == ItemQuestion
		switch {
		case qa && !qb:
			return -1
		case !qa && qb:
			return 1
		}
		return 0
	})
	return out
}

func itemLine(it Item, inList bool) string {
	text := strings.TrimSpace(it.Text)
	if inList {
		text = strings.ReplaceAll(text, "\n", "\n  ")
	}
	prefix := ""
	switch it.Kind {
	case ItemQuestion:
		prefix = "Question: "
	case ItemFailure:
		prefix = "Failed: "
	}
	if it.Title != "" {
		return prefix + it.Title + ": " + text
	}
	return prefix + text
}
