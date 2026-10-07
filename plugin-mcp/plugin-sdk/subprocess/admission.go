package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"sync"
	"time"
)

const ForwardHandlerSlots = 16
const ReverseHandlerSlots = 8
const ControlHandlerSlots = 2

type requestScopeKey struct{}

func scopeFromContext(ctx context.Context) *requestScope {
	s, _ := ctx.Value(requestScopeKey{}).(*requestScope)
	return s
}

// A scope separates terminal publication from actual execution completion. It is
// never found by ID when a callback replies: base IDs may already have been reused.
type requestScope struct {
	mu                       sync.Mutex
	id                       RPCID
	ctx                      context.Context
	cancel                   context.CancelCauseFunc
	credit                   *terminalCredit
	manager                  *admission
	terminal                 bool
	started                  bool
	completed                bool
	children                 int
	control                  bool
	executionDone            chan struct{}
	terminalDone             chan struct{}
	receiptOnce              sync.Once
	releaseOnce              sync.Once
	arrivedAt                time.Time
	binding                  *BindingID
	method                   string
	leaseEnd, leaseBudgetEnd time.Time
	leaseBudgets             HostRPCRemainingBudgets
	renewPending             bool
	observeTerminal          func(RPCResponse)
}
type admission struct {
	mu                sync.Mutex
	ordinary, control int
	active            map[RPCID]*requestScope
	writer            *frameWriter
	core              *correlation
	server            *server
	limits            AdmissionLimits
}

func newAdmission(w *frameWriter, c *correlation, s *server) *admission {
	return &admission{active: make(map[RPCID]*requestScope), writer: w, core: c, server: s, limits: AdmissionLimits{ForwardHandlerSlots, ReverseHandlerSlots, ControlHandlerSlots}}
}
func lifecycleMethod(method string) bool {
	return method == MethodInit || method == MethodLoad || method == MethodUnload
}
func (a *admission) begin(parent context.Context, req RPCRequest, received time.Time) (*requestScope, error) {
	control := lifecycleMethod(req.Method)
	a.mu.Lock()
	defer a.mu.Unlock()
	if control && a.control >= a.limits.Control || !control && a.ordinary >= a.limits.Forward {
		return nil, errPublicationFull
	}
	var credit *terminalCredit
	if req.ID != (RPCID{}) {
		// Preflight correlation into its reserved credit before author code runs.
		frame, err := a.server.encodeFrame(requestFailureResponse(req.ID, capability.BudgetExceeded, capability.Committed))
		if err != nil || len(frame) > TerminalCreditBytes {
			return nil, errPublicationFull
		}
		var errReserve error
		credit, errReserve = a.writer.reserveTerminal(TerminalCreditBytes)
		if errReserve != nil {
			return nil, errReserve
		}
	}
	ctx := parent
	forward, forwardErr := requestForwardContext(req)
	var deadlineCancel context.CancelFunc
	if forwardErr == nil && forward != nil {
		ctx, deadlineCancel = context.WithDeadlineCause(parent, received.Add(time.Duration(forward.TimeoutMS)*time.Millisecond), &DeadlineExceededError{})
	}
	ctx, cancel := context.WithCancelCause(ctx)
	s := &requestScope{method: req.Method, id: req.ID, ctx: ctx, cancel: cancel, credit: credit, manager: a, control: control, terminalDone: make(chan struct{}), executionDone: make(chan struct{}), arrivedAt: received}
	if forwardErr == nil && forward != nil && forward.BindingID != nil {
		binding := *forward.BindingID
		s.binding = &binding
	}
	s.ctx = context.WithValue(ctx, requestScopeKey{}, s)
	if control {
		a.control++
	} else {
		a.ordinary++
	}
	if req.ID != (RPCID{}) {
		a.active[req.ID] = s
	}
	if deadlineCancel != nil {
		context.AfterFunc(ctx, deadlineCancel)
	}
	context.AfterFunc(ctx, func() { s.failContext() })
	return s, nil
}
func (s *requestScope) failContext() {
	cause := context.Cause(s.ctx)
	var cancelled *TransportCancelledError
	var deadline *DeadlineExceededError
	if !errors.As(cause, &cancelled) && !errors.As(cause, &deadline) && !errors.Is(cause, context.DeadlineExceeded) {
		return
	}
	code := capability.Cancelled
	var expired *DeadlineExceededError
	if errors.As(cause, &expired) || errors.Is(cause, context.DeadlineExceeded) {
		code = capability.DeadlineExceeded
	}
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	state := capability.NotStarted
	if started {
		state = capability.Unknown
		if forwardMutationMethod(s.method) {
			code = capability.UnknownOutcome
		}
	}
	s.logicalReply(requestFailureResponse(s.id, code, state))
}
func (s *requestScope) start() bool {
	if end, ok := s.ctx.Deadline(); ok && !time.Now().Before(end) {
		s.cancel(&DeadlineExceededError{})
	}
	if errors.Is(s.ctx.Err(), context.DeadlineExceeded) || context.Cause(s.ctx) != nil {
		s.failContext()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return false
	}
	s.started = true
	return true
}
func (s *requestScope) reply(resp RPCResponse) {
	// A routed reply means author execution returned. Release that permit before
	// publication; retained inner hook work still delays executionDone.
	s.finish()
	s.logicalReply(resp)
}
func (s *requestScope) logicalReply(resp RPCResponse) {
	s.mu.Lock()
	if s.terminal {
		s.mu.Unlock()
		return
	}
	s.terminal = true
	observer := s.observeTerminal
	s.mu.Unlock()
	a := s.manager
	if s.id == (RPCID{}) {
		s.received(nil)
		return
	}
	if expired := s.responseCancellation(resp); expired != nil {
		resp = *expired
	}
	frame, err := a.server.encodeFrame(resp)
	if expired := s.responseCancellation(resp); expired != nil {
		resp = *expired
		frame, err = a.server.encodeFrame(resp)
	}
	if err == nil {
		value := resp
		err = a.writer.enqueueObserved(frame, s.received, true, s.credit, func() {
			if observer != nil {
				observer(value)
			}
		})
	}
	if err != nil {
		// A successful callback may have committed an effect. Preserve that fact when
		// its result cannot fit; admission failures happen before callback execution.
		state := responseEffect(resp)
		fallback := requestFailureResponse(s.id, capability.BudgetExceeded, state)
		frame, err = a.server.encodeFrame(fallback)
		if err == nil {
			err = a.writer.enqueueObserved(frame, s.received, true, s.credit, func() {
				if observer != nil {
					observer(fallback)
				}
			})
		}
		if err != nil {
			s.credit.release()
			s.received(err)
		}
	}
}

// Negotiation commits against the terminal actually selected for publication,
// before its bytes can reach a peer that immediately sends the next request.
func (s *requestScope) observeReply(fn func(RPCResponse)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return false
	}
	s.observeTerminal = fn
	return true
}
func (s *requestScope) received(err error) {
	defer s.receiptOnce.Do(func() { close(s.terminalDone) })
	a := s.manager
	a.mu.Lock()
	if a.active[s.id] == s {
		delete(a.active, s.id)
		a.core.release(s.id)
	}
	a.mu.Unlock()
	if err != nil {
		a.server.fence(err)
	}
}
func (s *requestScope) finish() {
	s.mu.Lock()
	s.completed = true
	release := s.children == 0
	s.mu.Unlock()
	if release {
		s.releasePermit()
	}
}
func (s *requestScope) retain() func() {
	s.mu.Lock()
	s.children++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.children--
			release := s.completed && s.children == 0
			s.mu.Unlock()
			if release {
				s.releasePermit()
			}
		})
	}
}
func (s *requestScope) releasePermit() {
	s.releaseOnce.Do(func() {
		a := s.manager
		a.mu.Lock()
		if s.control {
			a.control--
		} else {
			a.ordinary--
		}
		a.mu.Unlock()
		s.cancel(nil)
		close(s.executionDone)
	})
}
func (a *admission) cancelRequest(p CancelParams) {
	// Peer notifications can address only its own outgoing request domain.
	if p.RequestOwner != HostRPCOwnerHost {
		return
	}
	a.mu.Lock()
	s := a.active[p.ID]
	a.mu.Unlock()
	if s != nil {
		s.cancel(&TransportCancelledError{Reason: p.Reason})
	}
}
func retainRequestWork(ctx context.Context) func() {
	if s := scopeFromContext(ctx); s != nil {
		return s.retain()
	}
	return func() {}
}

func requestForwardContext(req RPCRequest) (*ForwardContext, error) {
	if req.Method == MethodInit {
		raw, err := paramsJSON(req.Params)
		if err != nil {
			return nil, err
		}
		var p InitParams
		err = json.Unmarshal(raw, &p)
		return p.Context, err
	}
	return validateRuntimeParams(req.Method, req.Params)
}
func responseEffect(resp RPCResponse) capability.EffectState {
	if resp.Error == nil {
		return capability.Committed
	}
	raw, err := json.Marshal(resp.Error.Data)
	if err == nil {
		var metadata struct {
			EffectState capability.EffectState `json:"effect_state"`
		}
		if json.Unmarshal(raw, &metadata) == nil && metadata.EffectState == capability.Committed {
			return capability.Committed
		}
	}
	return capability.Unknown
}

func (s *requestScope) responseCancellation(resp RPCResponse) *RPCResponse {
	cause := context.Cause(s.ctx)
	code := capability.Cancelled
	var cancelled *TransportCancelledError
	var expired *DeadlineExceededError
	timedOut := errors.As(cause, &expired) || errors.Is(cause, context.DeadlineExceeded)
	if end, ok := s.ctx.Deadline(); ok && !time.Now().Before(end) {
		timedOut = true
	}
	if timedOut {
		code = capability.DeadlineExceeded
	} else if !errors.As(cause, &cancelled) {
		return nil
	}
	state := responseEffect(resp)
	if resp.Error != nil && state != capability.Committed {
		s.mu.Lock()
		state = capability.NotStarted
		if s.started {
			state = capability.Unknown
			if forwardMutationMethod(s.method) {
				code = capability.UnknownOutcome
			}
		}
		s.mu.Unlock()
	}
	response := requestFailureResponse(s.id, code, state)
	return &response
}

func forwardMutationMethod(method string) bool {
	switch method {
	case MethodHealth, MethodCRUDRead, MethodCRUDList:
		return false
	case MethodInit, MethodLoad, MethodUnload, MethodCommandExecute, MethodEventHandle, MethodCRUDCreate, MethodCRUDUpdate, MethodCRUDDelete, MethodMCPCallTool, MethodHTTPHandle, MethodMigrate, MethodHookHandle, MethodHookHandleBatch:
		return true
	}
	return false
}

func (s *requestScope) acceptsResult() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	terminal := s.terminal
	s.mu.Unlock()
	if terminal {
		return false
	}
	if end, ok := s.ctx.Deadline(); ok && !time.Now().Before(end) {
		s.cancel(&DeadlineExceededError{})
		return false
	}
	cause := context.Cause(s.ctx)
	var cancelled *TransportCancelledError
	var deadline *DeadlineExceededError
	return !errors.As(cause, &cancelled) && !errors.As(cause, &deadline)
}
