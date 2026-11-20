package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/libs/agent/core/runloop"
	"github.com/hollis-labs/libs/agent/core/turn"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/hubbind"
)

// The ext keys on a tool call's part.end that tell the page to run it (WIRE.md).
const (
	ExtExec   = "ade.exec"
	ExtClient = "ade.client"
	ExtCall   = "ade.call"
)

// How long the page may go quiet while it runs a call (it posts tool-alive every few
// seconds), how long one call may take in all, and how long the person has to answer.
var (
	toolQuiet       = 45 * time.Second
	toolLongest     = 20 * time.Minute
	approvalTimeout = 10 * time.Minute
)

const (
	serverToolTimeout = 90 * time.Second
	maxToolResult     = 60_000 // characters handed back to the model from one call
)

var errCancelled = errors.New("cancelled")

type toolAnswer struct {
	content string
	isError bool
}

// pending is a call the page is running: where its answer goes, and where the page says
// it is still at it.
type pending struct {
	answer chan toolAnswer
	alive  chan struct{}
}

type run struct {
	h                         *Host
	id, owner, thread, client string
	tools                     map[string]ToolSpec
	server                    map[string]ServerTool
	defs                      []llmtypes.ToolDefinition
	cancel                    context.CancelCauseFunc
	prefix                    int // how large the last model request was, with its answer

	mu        sync.Mutex
	waiting   map[string]*pending    // tool call id -> the call the page is running
	approvals map[string]chan string // approval id -> the answer

	history     []llmtypes.ChatMessage
	in, out     int // tokens, summed over the run's steps
	msgN, partN int
}

func (r *run) emit(ev chatstream.Event) {
	ev.V = chatstream.SchemaVersion
	ev.RunID = r.id
	ev.Time = time.Now().UTC()
	// Not the run's context: a cancelled run's terminal event still has to go out.
	if _, err := hubbind.Publish(context.WithoutCancel(r.h.base), r.h.hub, r.id, ev); err != nil {
		slog.Warn("agenthost: event not published", "run", r.id, "verb", string(ev.Verb), "err", err)
	}
}

func meta(kv ...any) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		raw, _ := json.Marshal(kv[i+1])
		m[kv[i].(string)] = raw
	}
	return m
}

func (r *run) usage(scope chatstream.UsageScope) *chatstream.Usage {
	u, _ := chatstream.UsageFromExclusiveInput(scope, r.in, 0, 0, r.out)
	return &u
}

type step struct {
	res turn.TurnResult
	err error
}

func (r *run) execute(ctx context.Context) {
	h := r.h
	r.emit(chatstream.Event{Verb: chatstream.VerbRunStart, Provider: "agenthost", Model: h.cfg.Model})
	finish := chatstream.FinishStop
	var failure error
	steps, tries := 0, 0

	// A thread that has grown long is shortened for the model before it is called again.
	r.compact(ctx)

	_, loopErr := runloop.Execute(ctx, runloop.Ports[step, []llmtypes.ToolUseBlock]{
		// Request: one model invocation, streamed to the page as it arrives.
		Request: func(ctx context.Context) (step, runloop.Directive) {
			if ctx.Err() != nil {
				return step{}, runloop.Terminate
			}
			if steps >= h.cfg.MaxSteps {
				finish = chatstream.FinishTurnLimit
				return step{}, runloop.Finish
			}
			steps++
			r.msgN++
			msgID := fmt.Sprintf("m%d", r.msgN)
			r.emit(chatstream.Event{Verb: chatstream.VerbMessageStart, MessageID: msgID, Role: "assistant"})
			textPart := ""
			closeText := func() {
				if textPart != "" {
					r.emit(chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: textPart})
					textPart = ""
				}
			}
			res, err := turn.ExecuteTurn(ctx, turn.TurnRequest{
				IdleTimeout:     h.cfg.IdleTimeout,
				RequireTerminal: true,
				Stream: func(ctx context.Context) (<-chan llmtypes.StreamEvent, error) {
					return h.provider.StreamChat(ctx, llmtypes.ChatRequest{
						Model: h.cfg.Model, MaxTokens: h.cfg.MaxTokens,
						SystemPrompt: h.system(), Messages: r.history, Tools: r.defs,
					})
				},
				Sink: turn.TurnSink{
					OnDelta: func(s string) {
						if textPart == "" {
							r.partN++
							textPart = fmt.Sprintf("p%d", r.partN)
							r.emit(chatstream.Event{Verb: chatstream.VerbPartStart, PartID: textPart, Kind: string(chatstream.PartText)})
						}
						r.emit(chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: textPart, Text: s})
					},
					OnToolUse: func(llmtypes.ToolUseBlock) { closeText() },
				},
			})
			closeText()
			r.emit(chatstream.Event{Verb: chatstream.VerbMessageEnd, MessageID: msgID})
			if res.Usage != nil {
				in := res.Usage.InputTokens + res.Usage.CacheReadTokens + res.Usage.CacheCreationTokens
				r.in += in
				r.out += res.Usage.OutputTokens
				r.prefix = in + res.Usage.OutputTokens
				r.emit(chatstream.Event{Verb: chatstream.VerbUsage, Usage: r.usage(chatstream.UsageCumulative)})
			}
			if err != nil && ctx.Err() != nil {
				// Stopped mid-answer: what was said so far stays in the thread.
				if res.Text != "" {
					r.keep(ctx, "assistant", []llmtypes.ContentBlock{{Type: "text", Text: res.Text}})
				}
				return step{}, runloop.Terminate
			}
			// A request refused for a passing reason is tried again, while nothing of it
			// reached the page.
			var ge *GatewayError
			if err != nil && errors.As(err, &ge) && ge.Retryable() && res.Text == "" && tries < 2 {
				tries++
				steps--
				select {
				case <-time.After(time.Duration(tries) * 2 * time.Second):
					return step{}, runloop.Retry
				case <-ctx.Done():
					return step{}, runloop.Terminate
				}
			}
			tries = 0
			return step{res: res, err: err}, runloop.Proceed
		},
		// Consume: keep what the model said; decide whether there are tools to run.
		Consume: func(ctx context.Context, s step) ([]llmtypes.ToolUseBlock, runloop.Directive) {
			if s.err != nil {
				failure = s.err
				if s.res.Text != "" {
					r.keep(ctx, "assistant", []llmtypes.ContentBlock{{Type: "text", Text: s.res.Text}})
				}
				return nil, runloop.Terminate
			}
			blocks := []llmtypes.ContentBlock{}
			if s.res.Text != "" {
				blocks = append(blocks, llmtypes.ContentBlock{Type: "text", Text: s.res.Text})
			}
			for _, tu := range s.res.ToolUseBlocks {
				input := tu.Input
				if input == nil {
					input = map[string]any{}
				}
				blocks = append(blocks, llmtypes.ContentBlock{Type: "tool_use", ID: tu.ID, Name: tu.Name, Input: &input})
			}
			if len(blocks) > 0 {
				r.keep(ctx, "assistant", blocks)
			}
			if len(s.res.ToolUseBlocks) == 0 {
				switch s.res.StopReason {
				case llmtypes.StopReasonMaxTokens:
					finish = chatstream.FinishLength
				case llmtypes.StopReasonRefusal:
					finish = chatstream.FinishRefusal
				}
				return nil, runloop.Finish
			}
			return s.res.ToolUseBlocks, runloop.Proceed
		},
		// Settle: run the tools, one after another, and hand the model their results.
		Settle: func(ctx context.Context, calls []llmtypes.ToolUseBlock) runloop.Directive {
			results := make([]llmtypes.ContentBlock, 0, len(calls))
			for _, call := range calls {
				content, isError := r.tool(ctx, call)
				results = append(results, llmtypes.ContentBlock{Type: "tool_result", ToolUseID: call.ID, Content: content, IsError: isError})
			}
			// Kept even when cancelled mid-way, so every call in the thread has its answer.
			r.keep(context.WithoutCancel(ctx), "user", results)
			if ctx.Err() != nil {
				return runloop.Terminate
			}
			return runloop.Proceed
		},
		Finalize: func(context.Context) runloop.Directive { return runloop.Finish },
	})
	if failure == nil {
		failure = loopErr
	}
	r.end(ctx, finish, failure)
}

// end closes the run: its record and the stream's one terminal event.
func (r *run) end(ctx context.Context, finish chatstream.FinishReason, failure error) {
	st := r.h.store
	bg := context.WithoutCancel(ctx)
	rec := RunEnd{InputTokens: r.in, OutputTokens: r.out, PrefixTokens: r.prefix}
	switch {
	case errors.Is(context.Cause(ctx), errCancelled):
		rec.Status = "cancelled"
		_ = st.FinishRun(bg, r.id, rec)
		r.emit(chatstream.Event{Verb: chatstream.VerbRunAbort, Reason: string(chatstream.FinishCancelled)})
	case ctx.Err() != nil:
		rec.Status, rec.Reason = "error", "The host stopped while this was running."
		_ = st.FinishRun(bg, r.id, rec)
		r.emit(chatstream.Event{Verb: chatstream.VerbRunAbort, Reason: string(chatstream.FinishCancelled)})
	case failure != nil:
		slog.Warn("agenthost: run failed", "run", r.id, "err", failure)
		rec.Status, rec.Reason = "error", failure.Error()
		_ = st.FinishRun(bg, r.id, rec)
		code, retryable := "upstream_error", false
		var ge *GatewayError
		if errors.As(failure, &ge) {
			retryable = ge.Retryable()
		}
		if errors.Is(failure, turn.ErrTurnTruncated) {
			code = "upstream_truncated"
		}
		r.emit(chatstream.Event{Verb: chatstream.VerbRunError, Code: code, Retryable: retryable, Message: plain(failure)})
	default:
		rec.Status, rec.Reason = "finished", string(finish)
		_ = st.FinishRun(bg, r.id, rec)
		r.emit(chatstream.Event{Verb: chatstream.VerbRunFinish, Reason: string(finish), Usage: r.usage(chatstream.UsageFinal)})
	}
}

// plain is the error as a person should read it: without the turn package's wrapping.
func plain(err error) string {
	var te *turn.TurnError
	if errors.As(err, &te) && te.Err != nil {
		err = te.Err
	}
	if errors.Is(err, turn.ErrTurnStalled) {
		return "The model stopped answering."
	}
	if errors.Is(err, turn.ErrTurnTruncated) {
		return "The model's answer was cut off."
	}
	return err.Error()
}

func (r *run) keep(ctx context.Context, role string, blocks []llmtypes.ContentBlock) {
	r.history = append(r.history, llmtypes.ChatMessage{Role: role, ContentBlocks: blocks})
	if _, err := r.h.store.Append(context.WithoutCancel(ctx), r.thread, r.id, role, blocks, nil); err != nil {
		slog.Warn("agenthost: message not saved", "run", r.id, "err", err)
	}
}

// tool runs one call: it shows the call, asks first when the tool's level requires it,
// relays it to the page (or runs a server tool), and shows the result.
func (r *run) tool(ctx context.Context, call llmtypes.ToolUseBlock) (content string, isError bool) {
	r.partN++
	callPart := fmt.Sprintf("p%d", r.partN)
	spec, known := r.tools[call.Name]
	input, _ := json.Marshal(call.Input)
	if call.Input == nil {
		input = []byte("{}")
	}
	r.emit(chatstream.Event{Verb: chatstream.VerbPartStart, PartID: callPart, Kind: string(chatstream.PartToolCall),
		Meta: meta(chatstream.MetaName, call.Name, chatstream.MetaCallID, call.ID, "level", spec.Level)})

	defer func() {
		if len(content) > maxToolResult {
			content = content[:maxToolResult] + "\n[cut: the result was longer than this]"
		}
		content = strings.ToValidUTF8(content, "")
		r.partN++
		id := fmt.Sprintf("p%d", r.partN)
		r.emit(chatstream.Event{Verb: chatstream.VerbPartStart, PartID: id, Kind: string(chatstream.PartToolResult),
			Meta: meta(chatstream.MetaCallID, callPart, chatstream.MetaIsError, isError)})
		r.emit(chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: id, Text: content})
		r.emit(chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: id})
	}()

	if !known {
		r.emit(chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: callPart, Final: input})
		return fmt.Sprintf("There is no tool named %q.", call.Name), true
	}
	if spec.Level == LevelConfirm || (spec.Level == LevelWrite && !r.allowed(ctx, call.Name)) {
		decision := r.ask(ctx, call, input, spec.Level == LevelWrite)
		if spec.Level == LevelConfirm && (decision == "session" || decision == "always") {
			decision = "once" // no standing answer at this level, whatever the page sent
		}
		switch decision {
		case "session":
			r.h.mu.Lock()
			if r.h.session[r.owner] == nil {
				r.h.session[r.owner] = map[string]bool{}
			}
			r.h.session[r.owner][call.Name] = true
			r.h.mu.Unlock()
		case "always":
			if err := r.h.store.Grant(context.WithoutCancel(ctx), r.owner, call.Name); err != nil {
				slog.Warn("agenthost: permission not saved", "tool", call.Name, "err", err)
			}
		case "once":
		default:
			r.emit(chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: callPart, Final: input})
			switch decision {
			case "timeout":
				return "Nobody answered the permission question, so this did not run.", true
			case "cancelled":
				return "This did not run: the run was stopped.", true
			}
			return "The person declined this, so it did not run. Do not try it again; ask what they would like instead.", true
		}
	}

	// A tool of the host's own runs here.
	if st, ok := r.server[call.Name]; ok {
		r.emit(chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: callPart, Final: input, Ext: meta(ExtExec, "server")})
		toolCtx, cancel := context.WithTimeout(ctx, serverToolTimeout)
		defer cancel()
		args := call.Input
		if args == nil {
			args = map[string]any{}
		}
		out, err := st.Run(toolCtx, r.owner, args)
		switch {
		case ctx.Err() != nil:
			return "The run was stopped while this was in progress. Whether it ran is unknown.", true
		case errors.Is(err, context.DeadlineExceeded):
			return "This took too long and was given up on. Whether it ran is unknown; check before trying again.", true
		case err != nil:
			return err.Error(), true
		}
		return out, false
	}

	// The call's final event names who is to run it: the page that sent the message.
	wait := &pending{answer: make(chan toolAnswer, 1), alive: make(chan struct{}, 1)}
	r.mu.Lock()
	r.waiting[call.ID] = wait
	r.mu.Unlock()
	r.emit(chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: callPart, Final: input,
		Ext: meta(ExtExec, "client", ExtClient, r.client, ExtCall, call.ID)})
	quiet := time.NewTimer(toolQuiet)
	defer quiet.Stop()
	longest := time.NewTimer(toolLongest)
	defer longest.Stop()
	for {
		select {
		case a := <-wait.answer:
			return a.content, a.isError
		case <-wait.alive:
			quiet.Reset(toolQuiet)
		case <-quiet.C:
			// Forgetting the call is what makes this safe: a late answer is refused (409),
			// and the page's next tool-alive is told to stop.
			r.forget(call.ID)
			return "The page did not answer. It may have been closed. Whether this ran is unknown; check before trying again.", true
		case <-longest.C:
			r.forget(call.ID)
			return "This took too long and was given up on. Whether it ran is unknown; check before trying again.", true
		case <-ctx.Done():
			r.forget(call.ID)
			return "The run was stopped while this was in progress. Whether it ran is unknown.", true
		}
	}
}

func (r *run) forget(callID string) {
	r.mu.Lock()
	delete(r.waiting, callID)
	r.mu.Unlock()
}

func (r *run) allowed(ctx context.Context, tool string) bool {
	r.h.mu.Lock()
	ok := r.h.session[r.owner][tool]
	r.h.mu.Unlock()
	if ok {
		return true
	}
	ok, _ = r.h.store.Granted(ctx, r.owner, tool)
	return ok
}

// ask puts the permission question to the person and waits for the answer. standing says
// whether "session" and "always" are on offer.
func (r *run) ask(ctx context.Context, call llmtypes.ToolUseBlock, input []byte, standing bool) string {
	id := newID("apr")
	ch := make(chan string, 1)
	r.mu.Lock()
	r.approvals[id] = ch
	r.mu.Unlock()
	descriptor, _ := json.Marshal(map[string]any{"tool": call.Name, "input": json.RawMessage(input), "description": r.tools[call.Name].Description, "standing": standing})
	expires := time.Now().Add(approvalTimeout).UTC()
	r.emit(chatstream.Event{Verb: chatstream.VerbApprovalRequest, ApprovalID: id, CallID: call.ID,
		Reason: "This changes something.", Descriptor: descriptor, Mode: chatstream.ApprovalInBand, ExpiresAt: &expires})
	decision := ""
	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()
	select {
	case decision = <-ch:
	case <-timer.C:
		decision = "timeout"
	case <-ctx.Done():
		decision = "cancelled"
	}
	r.mu.Lock()
	delete(r.approvals, id)
	r.mu.Unlock()
	value, _ := json.Marshal(map[string]string{"approval_id": id, "decision": decision})
	r.emit(chatstream.Event{Verb: chatstream.VerbActivity, Kind: "ade.approval", Value: value})
	return decision
}

// repair makes a stored history sendable: a run that died between a tool call and its
// result left a call unanswered, which the model API refuses.
func repair(h []llmtypes.ChatMessage) []llmtypes.ChatMessage {
	out := make([]llmtypes.ChatMessage, 0, len(h))
	for i, m := range h {
		out = append(out, m)
		if m.Role != "assistant" {
			continue
		}
		answered := map[string]bool{}
		if i+1 < len(h) && h[i+1].Role == "user" {
			for _, b := range h[i+1].ContentBlocks {
				if b.Type == "tool_result" {
					answered[b.ToolUseID] = true
				}
			}
		}
		var missing []llmtypes.ContentBlock
		for _, b := range m.ContentBlocks {
			if b.Type == "tool_use" && !answered[b.ID] {
				missing = append(missing, llmtypes.ContentBlock{Type: "tool_result", ToolUseID: b.ID, Content: "This did not run: the conversation was interrupted.", IsError: true})
			}
		}
		if len(missing) == 0 {
			continue
		}
		if len(answered) > 0 {
			h[i+1].ContentBlocks = append(missing, h[i+1].ContentBlocks...)
			continue
		}
		out = append(out, llmtypes.ChatMessage{Role: "user", ContentBlocks: missing})
	}
	return out
}
