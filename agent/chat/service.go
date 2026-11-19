package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/hubbind"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// Defaults and limits.
const (
	// DefaultLeaseTTL is how long an input lease lasts without renewal. A
	// background browser tab runs timers about once a minute.
	DefaultLeaseTTL = 90 * time.Second
	// DefaultApprovalTTL is how long an approval waits for an answer when the
	// runner set no expires_at.
	DefaultApprovalTTL = 10 * time.Minute
	// DefaultCancelGrace is how long a canceled run may take to end before
	// the Service ends it with run.abort itself.
	DefaultCancelGrace = 10 * time.Second
	// MaxContextBytes bounds a turn's Context.
	MaxContextBytes = 64 << 10
	// MaxMetaBytes bounds a turn's Meta.
	MaxMetaBytes = 1 << 10

	requestMemo = 256 // request_ids remembered per conversation
)

// Agent is what a host knows about an agent it serves: the fields of its
// host.yaml the conversation record reports.
type Agent struct {
	// Owner is the single owner; it is what [Authorize] is asked about.
	Owner string
	// Scope is the seat ID of a stable agent or the application ID of an
	// embedded one.
	Scope string
	// Kind is reported as is and does not change how the wire behaves.
	Kind string
	// Runner runs this agent's turns. Nil uses Config.Runner.
	Runner Runner
}

// Config is everything a host supplies to a [Service].
type Config struct {
	// Agents resolves an agent name. It returns an error matching
	// [ErrNotFound] for an agent this host does not serve. Required.
	Agents func(ctx context.Context, agent string) (Agent, error)
	// Runner runs turns for every agent whose Agent.Runner is nil.
	Runner Runner
	// Log is the conversation event log: one stream per conversation, named
	// by the conversation ID. Required. The Service does not close it.
	Log streamhub.Log
	// Control stores conversations, attachments and the lease. Required.
	Control Control
	// Grants persists standing approvals. Nil means scope "always" is not
	// offered.
	Grants Grants
	// Intercept sees every submitted turn before it runs. Optional.
	Intercept Intercept
	// Clock defaults to the wall clock.
	Clock Clock
	// NewID issues conversation, run, attachment, message and part IDs from a
	// prefix. It defaults to [RandomID].
	NewID func(prefix string) string
	// LeaseTTL, ApprovalTTL and CancelGrace default to the constants above.
	LeaseTTL    time.Duration
	ApprovalTTL time.Duration
	CancelGrace time.Duration
	// StreamBuffer bounds the live events queued for one event stream before
	// it is closed as a slow consumer (streamhub's default when zero).
	StreamBuffer int
	// Logger receives failures that have no caller to return to. It is never
	// given a turn's text, context or meta. Nil discards.
	Logger *slog.Logger
}

// Service owns conversations: it checks the lease, starts runs, publishes
// runner events into the conversation stream and answers approvals. It does
// not authorize callers; [NewHandler] does that before it calls in. All
// methods are safe for concurrent use.
type Service struct {
	cfg  Config
	hub  *streamhub.Hub
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	mu       sync.Mutex
	convs    map[string]*conv
	watching map[<-chan string]bool
	closed   bool
}

// conv is what the Service holds in memory for one conversation. mu
// serializes every publish to its stream and every change to its state, so the
// order of events on the stream is the order things were decided in.
type conv struct {
	mu   sync.Mutex
	post []func() // run after mu is released

	id string
	// gen is the generation last read from Control and announced.
	gen uint64
	// orphan is a run the store says is active that this process never ran.
	orphan   string
	run      *activeRun
	requests map[string]memo
	order    []string
	// resolved holds the approvals of the last run that ended, so a late
	// second answer is still told it lost.
	resolved map[string]bool
	info     Info
	haveInfo bool
}

func (c *conv) unlock() {
	post := c.post
	c.post = nil
	c.mu.Unlock()
	for _, f := range post {
		f()
	}
}

type memo struct {
	digest  string
	receipt Receipt
}

type activeRun struct {
	id       string
	agent    string
	owner    string
	turn     Turn
	userMeta map[string]json.RawMessage
	showUser bool

	run             Run // nil until the runner accepted the turn
	preamble        bool
	cancelRequested bool
	ended           bool
	done            chan struct{}
	fin             Finalizer
	pending         map[string]*pendingApproval
	resolved        map[string]bool
	stopGrace       func() bool
}

type pendingApproval struct {
	request chatstream.Event
	stop    func() bool
}

// Receipt is what a submit returns.
type Receipt struct {
	RunID string `json:"run_id"`
	// Cursor is the conversation's head when the turn was admitted. Every
	// event of the run has a higher seq, so reading after Cursor yields the
	// whole run.
	Cursor uint64 `json:"cursor"`
}

// Submission is a turn submitted by an attachment.
type Submission struct {
	Attachment string
	Text       string
	Context    json.RawMessage
	Meta       json.RawMessage
	// RequestID makes a retry safe: a second submission with the same
	// RequestID and content returns the first one's Receipt.
	RequestID string
}

// NewService validates cfg and returns a running Service. Call Close to stop
// it.
func NewService(cfg Config) (*Service, error) {
	switch {
	case cfg.Agents == nil:
		return nil, errors.New("chat: Config.Agents is required")
	case cfg.Log == nil:
		return nil, errors.New("chat: Config.Log is required")
	case cfg.Control == nil:
		return nil, errors.New("chat: Config.Control is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock()
	}
	if cfg.NewID == nil {
		cfg.NewID = RandomID
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = DefaultLeaseTTL
	}
	if cfg.ApprovalTTL <= 0 {
		cfg.ApprovalTTL = DefaultApprovalTTL
	}
	if cfg.CancelGrace <= 0 {
		cfg.CancelGrace = DefaultCancelGrace
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Service{
		cfg: cfg, hub: streamhub.New(cfg.Log), ctx: ctx, stop: stop,
		convs: map[string]*conv{}, watching: map[<-chan string]bool{},
	}
	if cfg.Runner != nil {
		s.watch(cfg.Runner)
	}
	return s, nil
}

// Close ends every active run with run.abort (reason "shutdown"), stops the
// Service and waits, bounded by ctx, for its goroutines. The Log and the
// Control are left open.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	convs := make([]*conv, 0, len(s.convs))
	for _, c := range s.convs {
		convs = append(convs, c)
	}
	s.mu.Unlock()

	for _, c := range convs {
		c.mu.Lock()
		if c.run != nil {
			s.forceEndLocked(c, c.run, chatstream.Event{Verb: chatstream.VerbRunAbort, Reason: AbortShutdown})
		}
		c.unlock()
	}
	s.stop()
	err := s.hub.Shutdown(ctx)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		err = errors.Join(err, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("chat: close service: %w", err)
	}
	return nil
}

func (s *Service) now() time.Time { return s.cfg.Clock.Now() }

// watch starts one goroutine per distinct Replaced channel.
func (s *Service) watch(r Runner) {
	ch := r.Replaced()
	if ch == nil {
		return
	}
	s.mu.Lock()
	if s.watching[ch] || s.closed {
		s.mu.Unlock()
		return
	}
	s.watching[ch] = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		for {
			select {
			case id, ok := <-ch:
				if !ok {
					return
				}
				s.replaced(id)
			case <-s.ctx.Done():
				return
			}
		}
	}()
}

// Agent resolves an agent name through Config.Agents.
func (s *Service) Agent(ctx context.Context, agent string) (Agent, error) {
	if agent == "" {
		return Agent{}, invalid("agent is empty")
	}
	a, err := s.cfg.Agents(ctx, agent)
	if err != nil {
		return Agent{}, fmt.Errorf("chat: agent %q: %w", agent, err)
	}
	if a.Runner == nil {
		a.Runner = s.cfg.Runner
	}
	if a.Runner == nil {
		return Agent{}, fmt.Errorf("chat: agent %q has no runner", agent)
	}
	s.watch(a.Runner)
	return a, nil
}

// conv returns the in-memory state for a conversation the store knows.
func (s *Service) conv(ctx context.Context, id string) (*conv, error) {
	s.mu.Lock()
	closed, c := s.closed, s.convs[id]
	s.mu.Unlock()
	if closed {
		return nil, ErrServiceClosed
	}
	if c != nil {
		return c, nil
	}
	rec, err := s.cfg.Control.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("chat: load conversation: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c = s.convs[id]; c == nil {
		// A run the store calls active was running in a process that is gone.
		c = &conv{id: id, gen: rec.Generation, orphan: rec.ActiveRun, requests: map[string]memo{}}
		s.convs[id] = c
	}
	return c, nil
}

// with runs fn with the conversation locked and its record freshly read. It
// fails for a closed conversation, and notices a generation the Control
// raised since the last look.
func (s *Service) with(ctx context.Context, id string, fn func(c *conv, rec Conversation) error) error {
	c, err := s.conv(ctx, id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.unlock()
	rec, err := s.cfg.Control.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("chat: load conversation: %w", err)
	}
	if rec.Closed {
		return &ClosedError{ConversationID: id, Successor: rec.Successor}
	}
	if rec, err = s.reconcileLocked(ctx, c, rec); err != nil {
		return err
	}
	return fn(c, rec)
}

// reconcileLocked brings the in-memory state in line with the record: it ends
// a run left over from a dead process, and when the generation is higher than
// the one last seen it ends the active run and announces the new generation.
func (s *Service) reconcileLocked(ctx context.Context, c *conv, rec Conversation) (Conversation, error) {
	if c.orphan != "" {
		orphan := c.orphan
		c.orphan = ""
		if rec.ActiveRun == orphan {
			if _, err := s.publishLocked(c, chatstream.Event{Verb: chatstream.VerbRunAbort, RunID: orphan, Reason: AbortGeneration}); err != nil {
				return rec, err
			}
			var err error
			if b, ok := s.cfg.Control.(Bumper); ok {
				rec, err = b.Bump(ctx, c.id)
			} else if err = s.cfg.Control.EndRun(ctx, c.id, orphan); err == nil {
				rec, err = s.cfg.Control.Get(ctx, c.id)
			}
			if err != nil {
				return rec, fmt.Errorf("chat: end orphaned run: %w", err)
			}
		}
	}
	if rec.Generation <= c.gen {
		return rec, nil
	}
	if c.run != nil {
		s.forceEndLocked(c, c.run, chatstream.Event{Verb: chatstream.VerbRunAbort, Reason: AbortGeneration})
	}
	c.haveInfo = false
	if _, err := s.publishLocked(c, activity(ActivityGeneration, map[string]uint64{"generation": rec.Generation})); err != nil {
		return rec, err
	}
	c.gen = rec.Generation
	return rec, nil
}

// replaced handles a signal from Runner.Replaced: bump if the Control leaves
// that to the Service, then re-read the generation.
func (s *Service) replaced(id string) {
	c, err := s.conv(s.ctx, id)
	if err != nil {
		s.cfg.Logger.Warn("chat: replaced signal for a conversation that cannot be loaded", "conversation", id, "error", err)
		return
	}
	c.mu.Lock()
	defer c.unlock()
	var rec Conversation
	if b, ok := s.cfg.Control.(Bumper); ok {
		rec, err = b.Bump(s.ctx, id)
	} else {
		rec, err = s.cfg.Control.Get(s.ctx, id)
	}
	if err == nil && !rec.Closed {
		_, err = s.reconcileLocked(s.ctx, c, rec)
	}
	if err != nil && !errors.Is(err, ErrConversationClosed) {
		s.cfg.Logger.Error("chat: generation change failed", "conversation", id, "error", err)
	}
}

// publishLocked appends one event to the conversation stream. It uses the
// Service's own context: a sequence of events once begun is not cut short by a
// caller going away.
func (s *Service) publishLocked(c *conv, ev chatstream.Event) (chatstream.Event, error) {
	ev.V = chatstream.SchemaVersion
	if ev.Time.IsZero() {
		ev.Time = s.now().UTC()
	}
	out, err := hubbind.Publish(s.ctx, s.hub, c.id, ev)
	if err != nil {
		return out, fmt.Errorf("chat: publish %s: %w", ev.Verb, err)
	}
	return out, nil
}

func (s *Service) head(ctx context.Context, id string) (uint64, error) {
	h, err := s.cfg.Log.Head(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("chat: read log head: %w", err)
	}
	return uint64(h.Latest), nil
}

// ---- conversations ----

// EnsureConversation returns the agent's current conversation, creating it on
// first use.
func (s *Service) EnsureConversation(ctx context.Context, agent string) (View, error) {
	a, err := s.Agent(ctx, agent)
	if err != nil {
		return View{}, err
	}
	rec, _, err := s.cfg.Control.Ensure(ctx, NewConversation{
		ID: s.cfg.NewID("conv"), Agent: agent, Scope: a.Scope, Owner: a.Owner, Kind: a.Kind, At: s.now(),
	})
	if err != nil {
		return View{}, fmt.Errorf("chat: ensure conversation: %w", err)
	}
	return s.Conversation(ctx, rec.ID)
}

// Record returns the stored record of a conversation, closed or not. The
// handler uses it to learn the owner before it authorizes.
func (s *Service) Record(ctx context.Context, conversationID string) (Conversation, error) {
	rec, err := s.cfg.Control.Get(ctx, conversationID)
	if err != nil {
		return Conversation{}, fmt.Errorf("chat: load conversation: %w", err)
	}
	return rec, nil
}

// Conversation returns the current state of a conversation.
func (s *Service) Conversation(ctx context.Context, conversationID string) (View, error) {
	var v View
	err := s.with(ctx, conversationID, func(c *conv, rec Conversation) error {
		var err error
		v, err = s.viewLocked(ctx, c, rec)
		return err
	})
	return v, err
}

func (s *Service) viewLocked(ctx context.Context, c *conv, rec Conversation) (View, error) {
	head, err := s.head(ctx, c.id)
	if err != nil {
		return View{}, err
	}
	info := s.infoLocked(ctx, c, &rec)
	v := View{
		ConversationID: rec.ID, Agent: rec.Agent, Scope: rec.Scope, Owner: rec.Owner, Kind: rec.Kind,
		Generation: rec.Generation, RunnerRef: rec.RunnerRef, Head: head,
		Capabilities: Capabilities{Deltas: info.Deltas, Approvals: info.Approvals, Always: s.cfg.Grants != nil},
	}
	if l, ok := rec.LeaseAt(s.now()); ok {
		v.Lease = &LeaseView{Attachment: l.Holder, ExpiresAt: l.ExpiresAt}
	}
	if c.run != nil {
		id := c.run.id
		v.ActiveRun = &id
		for _, p := range c.run.pending {
			v.PendingApprovals = append(v.PendingApprovals, PendingApproval{
				ApprovalID: p.request.ApprovalID, RunID: id, CallID: p.request.CallID, Reason: p.request.Reason,
				Descriptor: p.request.Descriptor, ExpiresAt: p.request.ExpiresAt, Seq: p.request.Seq,
			})
		}
		sortApprovals(v.PendingApprovals)
	}
	return v, nil
}

// infoLocked asks the runner about the conversation and stores a changed
// RunnerRef. A runner that cannot answer leaves the last answer in place, and
// before any answer a client is told to expect deltas and approvals.
func (s *Service) infoLocked(ctx context.Context, c *conv, rec *Conversation) Info {
	if a, err := s.Agent(ctx, rec.Agent); err == nil {
		if info, err := a.Runner.Info(ctx, c.id); err == nil {
			c.info, c.haveInfo = info, true
		}
	}
	if !c.haveInfo {
		return Info{Deltas: true, Approvals: true, RunnerRef: rec.RunnerRef}
	}
	if ref := c.info.RunnerRef; ref != "" && ref != rec.RunnerRef {
		if err := s.cfg.Control.SetRunnerRef(ctx, c.id, ref); err != nil {
			s.cfg.Logger.Warn("chat: store runner ref failed", "conversation", c.id, "error", err)
		} else {
			rec.RunnerRef = ref
		}
	}
	return c.info
}

// Reset ends the agent's current conversation and returns a new one. The old
// log is kept and no longer served: its streams end with chat.closed. Only the
// lease holder may reset, or anyone when nobody holds the lease; attachmentID
// may then be empty. It returns ErrBusy while a run is active.
func (s *Service) Reset(ctx context.Context, agent, attachmentID string) (View, error) {
	a, err := s.Agent(ctx, agent)
	if err != nil {
		return View{}, err
	}
	next := NewConversation{ID: s.cfg.NewID("conv"), Agent: agent, Scope: a.Scope, Owner: a.Owner, Kind: a.Kind, At: s.now()}
	cur, created, err := s.cfg.Control.Ensure(ctx, next)
	if err != nil {
		return View{}, fmt.Errorf("chat: ensure conversation: %w", err)
	}
	if created {
		return s.Conversation(ctx, cur.ID) // nothing to end
	}
	var fresh Conversation
	err = s.with(ctx, cur.ID, func(c *conv, _ Conversation) error {
		if c.run != nil {
			return ErrBusy
		}
		var rerr error
		if fresh, rerr = s.cfg.Control.Reset(ctx, cur.ID, attachmentID, next, s.now()); rerr != nil {
			return fmt.Errorf("chat: reset: %w", rerr)
		}
		_, rerr = s.publishLocked(c, activity(ActivityClosed, map[string]string{
			"code": "conversation_closed", "conversation_id": fresh.ID,
		}))
		return rerr
	})
	if err != nil {
		return View{}, err
	}
	s.mu.Lock()
	delete(s.convs, cur.ID)
	s.mu.Unlock()
	return s.Conversation(ctx, fresh.ID)
}

// ---- attachments and the lease ----

// Attach adds a view of the conversation and returns it with the head to
// start reading from.
func (s *Service) Attach(ctx context.Context, conversationID string, role Role, mode StreamMode) (Attachment, uint64, error) {
	if !role.Valid() {
		return Attachment{}, 0, invalid("role must be %q or %q", RoleViewer, RoleController)
	}
	if !mode.Valid() {
		return Attachment{}, 0, invalid("stream must be %q or %q", StreamDeltas, StreamCommitted)
	}
	var (
		att  Attachment
		head uint64
	)
	err := s.with(ctx, conversationID, func(c *conv, _ Conversation) error {
		var err error
		att, err = s.cfg.Control.Attach(ctx, Attachment{
			ID: s.cfg.NewID("att"), ConversationID: conversationID, Role: role, Stream: mode, CreatedAt: s.now(),
		})
		if err != nil {
			return fmt.Errorf("chat: attach: %w", err)
		}
		ev, err := s.publishLocked(c, activity(ActivityAttachment, map[string]string{
			"state": "attached", "attachment": att.ID, "role": string(role), "stream": string(mode),
		}))
		head = ev.Seq
		return err
	})
	return att, head, err
}

// attachmentRole reports the role of a current attachment, for the handler to
// choose the permission a route needs. Any failure is left for the operation
// itself to report.
func (s *Service) attachmentRole(ctx context.Context, conversationID, attachmentID string) (Role, bool) {
	att, err := s.cfg.Control.Attachment(ctx, conversationID, attachmentID)
	return att.Role, err == nil
}

// Detach removes an attachment and releases the lease if it held it. It never
// stops a run.
func (s *Service) Detach(ctx context.Context, conversationID, attachmentID string) error {
	return s.with(ctx, conversationID, func(c *conv, _ Conversation) error {
		att, err := s.cfg.Control.Attachment(ctx, conversationID, attachmentID)
		if err != nil {
			return fmt.Errorf("chat: detach: %w", err)
		}
		released, err := s.cfg.Control.Detach(ctx, conversationID, attachmentID, s.now())
		if err != nil {
			return fmt.Errorf("chat: detach: %w", err)
		}
		if _, err = s.publishLocked(c, activity(ActivityAttachment, map[string]string{
			"state": "detached", "attachment": att.ID, "role": string(att.Role), "stream": string(att.Stream),
		})); err != nil {
			return err
		}
		if released {
			_, err = s.publishLocked(c, activity(ActivityLease, map[string]string{"state": "released", "attachment": att.ID}))
		}
		return err
	})
}

func (s *Service) leaseChangedLocked(c *conv, ch LeaseChange) error {
	if !ch.Acquired {
		return nil
	}
	v := map[string]string{"state": "acquired", "attachment": ch.Lease.Holder}
	if ch.Previous != "" {
		v["previous"] = ch.Previous
	}
	_, err := s.publishLocked(c, activity(ActivityLease, v))
	return err
}

// AcquireLease acquires the input lease for a controller attachment, or
// renews it.
func (s *Service) AcquireLease(ctx context.Context, conversationID, attachmentID string) (Lease, error) {
	return s.lease(ctx, conversationID, attachmentID, s.cfg.Control.Acquire)
}

// TakeoverLease takes the input lease from whoever holds it. The caller must
// have checked the takeover permission.
func (s *Service) TakeoverLease(ctx context.Context, conversationID, attachmentID string) (Lease, error) {
	return s.lease(ctx, conversationID, attachmentID, s.cfg.Control.Takeover)
}

func (s *Service) lease(ctx context.Context, conversationID, attachmentID string,
	op func(context.Context, string, string, time.Time, time.Duration) (LeaseChange, error)) (Lease, error) {
	var l Lease
	err := s.with(ctx, conversationID, func(c *conv, _ Conversation) error {
		ch, err := op(ctx, conversationID, attachmentID, s.now(), s.cfg.LeaseTTL)
		if err != nil {
			return fmt.Errorf("chat: lease: %w", err)
		}
		l = ch.Lease
		return s.leaseChangedLocked(c, ch)
	})
	return l, err
}

// ---- events ----

// Events opens an attachment's event stream at from. An attachment from an
// older generation is refused with a *StaleError.
func (s *Service) Events(ctx context.Context, conversationID, attachmentID string, from Cursor) (*Stream, error) {
	var st *Stream
	err := s.with(ctx, conversationID, func(c *conv, _ Conversation) error {
		att, err := s.cfg.Control.Attachment(ctx, conversationID, attachmentID)
		if err != nil {
			return fmt.Errorf("chat: events: %w", err)
		}
		head, err := s.head(ctx, conversationID)
		if err != nil {
			return err
		}
		active := ""
		if c.run != nil {
			active = c.run.id
		}
		after := streamhub.Seq(from.seq)
		if from.head {
			after = streamhub.FromLatest
		}
		sub, err := s.hub.Subscribe(ctx, conversationID, streamhub.SubscribeOptions{
			After: after, Buffer: s.cfg.StreamBuffer,
			Filter: deltaFilter(att.Stream, streamhub.Seq(head), active),
		})
		if err != nil {
			return fmt.Errorf("chat: subscribe: %w", err)
		}
		st = &Stream{sub: sub, generation: att.Generation, now: s.now, last: from.seq}
		if from.head {
			st.last = head
		}
		return nil
	})
	return st, err
}

// ---- turns ----

func validateTurn(text string, turnContext, meta json.RawMessage) error {
	if text == "" {
		return invalid("text is empty")
	}
	if len(turnContext) > MaxContextBytes {
		return invalid("context is larger than %d bytes", MaxContextBytes)
	}
	if len(turnContext) > 0 && !json.Valid(turnContext) {
		return invalid("context is not valid JSON")
	}
	if len(meta) > MaxMetaBytes {
		return invalid("meta is larger than %d bytes", MaxMetaBytes)
	}
	if _, err := metaObject(meta); err != nil {
		return err
	}
	return nil
}

// metaObject decodes a turn's meta, which must be a JSON object or absent.
func metaObject(meta json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(meta) == 0 || string(meta) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(meta, &out); err != nil || out == nil {
		return nil, invalid("meta must be a JSON object")
	}
	return out, nil
}

// SubmitAs submits a turn on behalf of an attachment. The attachment must
// hold the input lease, or be a controller while nobody holds it, in which
// case it acquires the lease. It returns ErrLeaseRequired otherwise, ErrBusy
// while a run is active, and a *RejectedError when the host's Intercept
// refuses the turn.
func (s *Service) SubmitAs(ctx context.Context, conversationID string, in Submission) (Receipt, error) {
	if in.Attachment == "" {
		return Receipt{}, invalid("attachment is empty")
	}
	turn := Turn{Text: in.Text, Context: in.Context, Meta: in.Meta}
	return s.submit(ctx, conversationID, in.Attachment, in.RequestID, turn)
}

// Submit starts a turn from inside the host: a reflex, a trigger, a message
// from another agent. It needs no attachment and no lease. It goes through
// the same Intercept and obeys the same rule of one active run per
// conversation, returning ErrBusy otherwise.
func (s *Service) Submit(ctx context.Context, conversationID string, turn Turn) (Receipt, error) {
	return s.submit(ctx, conversationID, "", "", turn)
}

func (s *Service) submit(ctx context.Context, conversationID, attachmentID, requestID string, turn Turn) (Receipt, error) {
	if err := validateTurn(turn.Text, turn.Context, turn.Meta); err != nil {
		return Receipt{}, err
	}
	digest := string(mustJSON([]any{turn.Text, string(turn.Context), string(turn.Meta)}))
	var (
		receipt Receipt
		again   bool
		agent   string
	)
	// Look before calling the hook, so a turn that cannot run does not reach it.
	err := s.with(ctx, conversationID, func(c *conv, rec Conversation) error {
		agent = rec.Agent
		if attachmentID != "" {
			att, err := s.cfg.Control.Attachment(ctx, conversationID, attachmentID)
			if err != nil {
				return fmt.Errorf("chat: submit: %w", err)
			}
			var done bool
			if receipt, done, err = c.retried(requestID, digest); done || err != nil {
				again = done
				return err
			}
			held, ok := rec.LeaseAt(s.now())
			if (ok && held.Holder != att.ID) || (!ok && att.Role != RoleController) {
				return ErrLeaseRequired
			}
		}
		if c.run != nil {
			return ErrBusy
		}
		return nil
	})
	if err != nil || again {
		return receipt, err
	}
	a, err := s.Agent(ctx, agent)
	if err != nil {
		return Receipt{}, err
	}

	turn.ConversationID, turn.RunID = conversationID, s.cfg.NewID("run")
	out := Continue()
	if s.cfg.Intercept != nil {
		runID := turn.RunID
		out = s.cfg.Intercept(ctx, &turn)
		turn.ConversationID, turn.RunID = conversationID, runID
	}
	if out.kind == outcomeReject {
		return Receipt{}, &RejectedError{Code: out.code, Message: out.message}
	}
	meta, err := metaObject(turn.Meta)
	if err != nil {
		return Receipt{}, fmt.Errorf("chat: meta after Intercept: %w", err)
	}
	if requestID != "" {
		meta["request_id"] = mustJSON(requestID)
	}

	// Admit the turn. The lease and the one-run rule are checked again here,
	// in one step with taking the run slot: this is the moment that counts.
	var (
		ar *activeRun
		c  *conv
	)
	actx := context.WithoutCancel(ctx)
	err = s.with(actx, conversationID, func(cc *conv, rec Conversation) error {
		c = cc
		prior, done, aerr := c.retried(requestID, digest)
		if done || aerr != nil {
			receipt, again = prior, done
			return aerr
		}
		ch, aerr := s.cfg.Control.BeginRun(actx, conversationID, turn.RunID, attachmentID, s.now(), s.cfg.LeaseTTL)
		if aerr != nil {
			return fmt.Errorf("chat: submit: %w", aerr)
		}
		ar = newRun(turn, rec, meta, true)
		c.run = ar
		if aerr = s.leaseChangedLocked(c, ch); aerr != nil {
			s.finishLocked(c, ar)
			return aerr
		}
		head, aerr := s.head(actx, conversationID)
		if aerr != nil {
			s.finishLocked(c, ar)
			return aerr
		}
		receipt = Receipt{RunID: turn.RunID, Cursor: head}
		c.remember(requestID, memo{digest: digest, receipt: receipt})
		if out.kind == outcomeAnswer {
			return s.answerLocked(c, ar, out.text)
		}
		return nil
	})
	if err != nil || again {
		return receipt, err
	}
	if out.kind == outcomeAnswer {
		if rerr := a.Runner.Record(actx, turn, out.text); rerr != nil {
			s.cfg.Logger.Error("chat: runner did not record a host answer", "conversation", conversationID, "run", turn.RunID, "error", rerr)
		}
		return receipt, nil
	}

	// The slot is held; start the runner without the conversation locked.
	run, err := a.Runner.Start(s.ctx, turn)
	c.mu.Lock()
	defer c.unlock()
	if err != nil || run == nil {
		if !ar.ended {
			c.forget(requestID)
			s.finishLocked(c, ar)
		}
		if err == nil {
			err = errors.New("runner returned no run")
		}
		return Receipt{}, fmt.Errorf("%w: start: %w", ErrRunner, err)
	}
	ar.run = run
	if ar.ended || ar.cancelRequested {
		c.post = append(c.post, run.Cancel)
	}
	if ar.ended {
		// Ended while starting (a generation change): the stream already says so.
		c.post = append(c.post, func() { drain(run) })
		return receipt, nil
	}
	s.wg.Add(1)
	go s.pump(c, ar)
	return receipt, nil
}

// drain reads a run nobody will publish to its end, so its producer is not
// left blocked.
func drain(run Run) {
	for range run.Events() { //nolint:revive // the events are discarded on purpose
	}
}

func newRun(turn Turn, rec Conversation, meta map[string]json.RawMessage, showUser bool) *activeRun {
	return &activeRun{
		id: turn.RunID, agent: rec.Agent, owner: rec.Owner, turn: turn, userMeta: meta, showUser: showUser,
		done: make(chan struct{}), pending: map[string]*pendingApproval{}, resolved: map[string]bool{},
	}
}

func (c *conv) retried(requestID, digest string) (Receipt, bool, error) {
	if requestID == "" {
		return Receipt{}, false, nil
	}
	m, ok := c.requests[requestID]
	if !ok {
		return Receipt{}, false, nil
	}
	if m.digest != digest {
		return Receipt{}, false, ErrRequestConflict
	}
	return m.receipt, true, nil
}

func (c *conv) remember(requestID string, m memo) {
	if requestID == "" {
		return
	}
	c.requests[requestID] = m
	c.order = append(c.order, requestID)
	if len(c.order) > requestMemo {
		delete(c.requests, c.order[0])
		c.order = c.order[1:]
	}
}

func (c *conv) forget(requestID string) {
	if requestID != "" {
		delete(c.requests, requestID)
	}
}

// Observe publishes a run that began on the runner's side. turn.Text is what
// was typed or injected, if known; run yields the run's events as usual.
//
// The Service issues the run_id. The user's message is published from
// turn.Text when it is not empty, with meta origin "runner". No lease is
// needed and the Intercept is not called. One active run still holds: if
// another run is being published, Observe waits for its terminal event,
// bounded by ctx. It returns once the run is adopted; its events are published
// as they arrive.
func (s *Service) Observe(ctx context.Context, conversationID string, turn Turn, run Run) error {
	if run == nil {
		return invalid("run is nil")
	}
	meta, err := metaObject(turn.Meta)
	if err != nil {
		return err
	}
	meta["origin"] = mustJSON(OriginRunner)
	for {
		var wait chan struct{}
		err := s.with(ctx, conversationID, func(c *conv, rec Conversation) error {
			if c.run != nil {
				wait = c.run.done
				return nil
			}
			turn.ConversationID, turn.RunID = conversationID, s.cfg.NewID("run")
			if _, err := s.cfg.Control.BeginRun(ctx, conversationID, turn.RunID, "", s.now(), s.cfg.LeaseTTL); err != nil {
				return fmt.Errorf("chat: observe: %w", err)
			}
			ar := newRun(turn, rec, meta, turn.Text != "")
			ar.run = run
			c.run = ar
			s.wg.Add(1)
			go s.pump(c, ar)
			return nil
		})
		if err != nil || wait == nil {
			return err
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ctx.Done():
			return ErrServiceClosed
		}
	}
}

// Cancel asks the active run to stop. The attachment must hold the lease. The
// run ends with run.abort.
func (s *Service) Cancel(ctx context.Context, conversationID, runID, attachmentID string) error {
	return s.with(ctx, conversationID, func(c *conv, _ Conversation) error {
		if err := s.cfg.Control.Holds(ctx, conversationID, attachmentID, s.now()); err != nil {
			return fmt.Errorf("chat: cancel: %w", err)
		}
		ar := c.run
		if ar == nil || ar.id != runID {
			return ErrRunNotActive
		}
		if ar.cancelRequested {
			return nil
		}
		ar.cancelRequested = true
		if ar.run != nil {
			c.post = append(c.post, ar.run.Cancel)
		}
		ar.stopGrace = s.cfg.Clock.AfterFunc(s.cfg.CancelGrace, func() {
			c.mu.Lock()
			defer c.unlock()
			s.forceEndLocked(c, ar, chatstream.Event{Verb: chatstream.VerbRunAbort, Reason: AbortCanceled})
		})
		return nil
	})
}

// ---- runs ----

// preambleLocked publishes what opens every run on the stream: run.start,
// then the user's message.
func (s *Service) preambleLocked(c *conv, ar *activeRun, start chatstream.Event) error {
	ar.preamble = true
	start.Verb, start.RunID = chatstream.VerbRunStart, ar.id
	if _, err := s.publishLocked(c, start); err != nil {
		return err
	}
	if !ar.showUser {
		return nil
	}
	for _, ev := range messageEvents(s.cfg.NewID("msg"), s.cfg.NewID("part"), "user", ar.turn.Text, ar.userMeta) {
		ev.RunID = ar.id
		if _, err := s.publishLocked(c, ev); err != nil {
			return err
		}
	}
	return nil
}

// answerLocked publishes a turn the host answered as a whole run.
func (s *Service) answerLocked(c *conv, ar *activeRun, text string) error {
	defer s.finishLocked(c, ar)
	if err := s.preambleLocked(c, ar, chatstream.Event{Provider: ProviderHost}); err != nil {
		return err
	}
	evs := messageEvents(s.cfg.NewID("msg"), s.cfg.NewID("part"), "assistant", text, nil)
	evs = append(evs, chatstream.Event{Verb: chatstream.VerbRunFinish, Reason: string(chatstream.FinishStop)})
	for _, ev := range evs {
		ev.RunID = ar.id
		if _, err := s.publishLocked(c, ev); err != nil {
			return err
		}
	}
	return nil
}

// finishLocked releases the run slot.
func (s *Service) finishLocked(c *conv, ar *activeRun) {
	if ar.ended {
		return
	}
	ar.ended = true
	for _, p := range ar.pending {
		p.stop()
	}
	ar.pending = nil
	if ar.stopGrace != nil {
		ar.stopGrace()
	}
	if c.run == ar {
		c.run, c.resolved = nil, ar.resolved
	}
	if err := s.cfg.Control.EndRun(s.ctx, c.id, ar.id); err != nil && !errors.Is(err, ErrConversationClosed) {
		s.cfg.Logger.Error("chat: end run failed", "conversation", c.id, "run", ar.id, "error", err)
	}
	close(ar.done)
}

// forceEndLocked ends a run with a terminal event of the Service's own,
// whatever the runner does next, and asks the runner to stop.
func (s *Service) forceEndLocked(c *conv, ar *activeRun, terminal chatstream.Event) {
	if ar.ended {
		return
	}
	var err error
	if !ar.preamble {
		err = s.preambleLocked(c, ar, chatstream.Event{})
	}
	if err == nil {
		terminal.RunID = ar.id
		_, err = s.publishLocked(c, terminal)
	}
	if err != nil {
		s.cfg.Logger.Error("chat: could not publish the end of a run", "conversation", c.id, "run", ar.id, "error", err)
	}
	s.finishLocked(c, ar)
	if ar.run != nil {
		c.post = append(c.post, ar.run.Cancel)
	}
}

// pump publishes a run's events until its terminal event.
func (s *Service) pump(c *conv, ar *activeRun) {
	defer s.wg.Done()
	for ev, err := range ar.run.Events() {
		if err != nil {
			c.mu.Lock()
			s.cfg.Logger.Error("chat: runner event stream failed", "conversation", c.id, "run", ar.id, "error", err)
			s.forceEndLocked(c, ar, chatstream.Event{
				Verb: chatstream.VerbRunError, Code: CodeRunnerError, Message: "the runner's event stream failed",
			})
			c.unlock()
			return
		}
		if s.handle(c, ar, ev) {
			return
		}
	}
	// The runner stopped without a terminal event.
	c.mu.Lock()
	defer c.unlock()
	terminal := chatstream.Event{Verb: chatstream.VerbRunError, Code: chatstream.CodeStreamLost, Message: "the runner ended the run without a terminal event"}
	if ar.cancelRequested {
		terminal = chatstream.Event{Verb: chatstream.VerbRunAbort, Reason: AbortCanceled}
	}
	s.forceEndLocked(c, ar, terminal)
}

// handle publishes one runner event and reports whether the run is over.
func (s *Service) handle(c *conv, ar *activeRun, ev chatstream.Event) (done bool) {
	c.mu.Lock()
	defer c.unlock()
	if ar.ended {
		return true
	}
	// The run_id is the Service's, whatever the runner set; the hub assigns seq.
	ev.RunID, ev.Seq = ar.id, 0
	fail := func(err error) bool {
		s.cfg.Logger.Error("chat: publish failed; ending the run", "conversation", c.id, "run", ar.id, "error", err)
		s.finishLocked(c, ar)
		c.post = append(c.post, ar.run.Cancel)
		return true
	}
	if !ar.preamble {
		start := chatstream.Event{}
		if ev.Verb == chatstream.VerbRunStart {
			start = ev
		}
		if err := s.preambleLocked(c, ar, start); err != nil {
			return fail(err)
		}
		if ev.Verb == chatstream.VerbRunStart {
			return false
		}
	} else if ev.Verb == chatstream.VerbRunStart {
		return false // a run starts once
	}
	ev = ar.fin.Apply(ev)

	switch {
	case ev.Verb == chatstream.VerbApprovalRequest:
		if ev.Mode == "" {
			ev.Mode = chatstream.ApprovalInBand
		}
		if ev.ExpiresAt == nil {
			t := s.now().Add(s.cfg.ApprovalTTL)
			ev.ExpiresAt = &t
		}
	case ev.Verb == chatstream.VerbActivity && ev.Kind == ActivityApproval:
		// The runner resolved an approval itself. Publish its outcome unless
		// the Service already published one for the same approval.
		id := approvalID(ev)
		if ar.resolved[id] {
			return false
		}
		if p := ar.pending[id]; p != nil {
			p.stop()
			delete(ar.pending, id)
		}
		ar.resolved[id] = true
	}

	out, err := s.publishLocked(c, ev)
	if err != nil {
		return fail(err)
	}
	if out.Verb == chatstream.VerbApprovalRequest && out.ApprovalID != "" && !ar.resolved[out.ApprovalID] {
		s.trackApprovalLocked(c, ar, out)
	}
	if out.IsTerminal() {
		s.finishLocked(c, ar)
		return true
	}
	return false
}

func approvalID(ev chatstream.Event) string {
	if ev.ApprovalID != "" {
		return ev.ApprovalID
	}
	var v struct {
		ApprovalID string `json:"approval_id"`
	}
	_ = json.Unmarshal(ev.Value, &v) // a value without the field yields ""
	return v.ApprovalID
}

// ---- approvals ----

func (s *Service) trackApprovalLocked(c *conv, ar *activeRun, req chatstream.Event) {
	if old := ar.pending[req.ApprovalID]; old != nil {
		old.stop()
	}
	id := req.ApprovalID
	p := &pendingApproval{request: req}
	p.stop = s.cfg.Clock.AfterFunc(req.ExpiresAt.Sub(s.now()), func() {
		c.mu.Lock()
		defer c.unlock()
		if ar.ended || ar.pending[id] != p {
			return
		}
		s.resolveLocked(c, ar, p, Decision{Scope: ScopeOnce, Reason: ReasonExpired}, "")
	})
	ar.pending[id] = p
}

// resolveLocked publishes an approval's outcome and hands the decision to the
// runner once the conversation is unlocked. First answer wins: the approval is
// no longer pending when this returns.
func (s *Service) resolveLocked(c *conv, ar *activeRun, p *pendingApproval, d Decision, attachmentID string) {
	id := p.request.ApprovalID
	p.stop()
	delete(ar.pending, id)
	ar.resolved[id] = true
	v := map[string]string{"approval_id": id, "decision": "deny", "scope": string(d.Scope)}
	if d.Allow {
		v["decision"] = "allow"
	}
	if attachmentID != "" {
		v["attachment"] = attachmentID
	}
	if d.Reason != "" {
		v["reason"] = d.Reason
	}
	ev := activity(ActivityApproval, v)
	ev.RunID, ev.ApprovalID, ev.CallID = ar.id, id, p.request.CallID
	if _, err := s.publishLocked(c, ev); err != nil {
		s.cfg.Logger.Error("chat: could not publish an approval outcome", "conversation", c.id, "run", ar.id, "error", err)
	}
	if d.Reason == ReasonExpired && ar.run != nil {
		run := ar.run
		c.post = append(c.post, func() {
			if err := run.Answer(s.ctx, id, d); err != nil {
				s.cfg.Logger.Error("chat: runner refused an expired approval", "conversation", c.id, "run", ar.id, "error", err)
			}
		})
	}
}

// standing reports whether an approval's descriptor allows a grant beyond
// this call. Only an explicit "standing": false forbids it.
func standing(descriptor json.RawMessage) bool {
	var d struct {
		Standing *bool `json:"standing"`
	}
	_ = json.Unmarshal(descriptor, &d) // an unreadable descriptor forbids nothing
	return d.Standing == nil || *d.Standing
}

// AnswerApproval answers a pending approval of the active run. The attachment
// must hold the lease when the answer is applied. First answer wins: a second
// one gets ErrApprovalResolved. Scope "always" needs Config.Grants, and an
// approval whose descriptor says "standing": false takes only scope "once";
// otherwise ErrScopeUnavailable.
func (s *Service) AnswerApproval(ctx context.Context, conversationID, approvalID, attachmentID string, d Decision) error {
	if d.Scope == "" {
		d.Scope = ScopeOnce
	}
	if !d.Scope.Valid() {
		return invalid("scope must be %q, %q or %q", ScopeOnce, ScopeConversation, ScopeAlways)
	}
	if !d.Allow {
		d.Scope = ScopeOnce
	}
	d.Reason = ""
	var run Run
	err := s.with(ctx, conversationID, func(c *conv, rec Conversation) error {
		if err := s.cfg.Control.Holds(ctx, conversationID, attachmentID, s.now()); err != nil {
			return fmt.Errorf("chat: approval: %w", err)
		}
		ar := c.run
		if ar == nil {
			if c.resolved[approvalID] {
				return ErrApprovalResolved
			}
			return ErrUnknownApproval
		}
		p := ar.pending[approvalID]
		switch {
		case p == nil && ar.resolved[approvalID]:
			return ErrApprovalResolved
		case p == nil:
			return ErrUnknownApproval
		case !s.now().Before(*p.request.ExpiresAt):
			s.resolveLocked(c, ar, p, Decision{Scope: ScopeOnce, Reason: ReasonExpired}, "")
			return ErrApprovalResolved
		}
		if d.Scope != ScopeOnce && !standing(p.request.Descriptor) {
			return fmt.Errorf("%w: this approval is asked every time", ErrScopeUnavailable)
		}
		if d.Scope == ScopeAlways {
			if s.cfg.Grants == nil {
				return fmt.Errorf("%w: this host keeps no standing grants", ErrScopeUnavailable)
			}
			if err := s.cfg.Grants.Grant(ctx, Grant{
				ConversationID: conversationID, Agent: rec.Agent, Owner: rec.Owner, RunID: ar.id,
				ApprovalID: approvalID, CallID: p.request.CallID, Reason: p.request.Reason, Descriptor: p.request.Descriptor,
			}); err != nil {
				return fmt.Errorf("chat: store standing grant: %w", err)
			}
		}
		s.resolveLocked(c, ar, p, d, attachmentID)
		run = ar.run
		return nil
	})
	if err != nil {
		return err
	}
	if err := run.Answer(context.WithoutCancel(ctx), approvalID, d); err != nil {
		return fmt.Errorf("%w: answer approval: %w", ErrRunner, err)
	}
	return nil
}
