package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	agentcontext "github.com/hollis-labs/libs/agent/core/context"
	"github.com/hollis-labs/libs/agent/core/runloop"
	"github.com/hollis-labs/libs/agent/core/turn"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// Run executes the actual native loop with fresh per-run context and no retries.
// Cancellation stops waits/queued effects, preserves pending ticket identity and
// emits terminal evidence using a separate bounded observation context. Hosts
// must reconcile effects already admitted; cancellation cannot undo them.
func (o *Operative) Run(ctx context.Context, input Input) (result Result, runErr error) {
	result = Result{RunID: input.ID, Binding: input.Binding, Status: Failed}
	if !o.busy.CompareAndSwap(false, true) {
		return result, ErrBusy
	}
	defer o.busy.Store(false)
	in, err := clone(input)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(in.ID) == "" || strings.TrimSpace(in.Text) == "" {
		return result, fmt.Errorf("%w: run ID and text required", ErrInvalid)
	}
	if err = in.Binding.Validate(); err != nil {
		return result, err
	}
	route, err := o.ports.Router.selectRoute(in.Route, len(o.tools) > 0)
	if err != nil {
		return result, err
	}
	events := runEvents{emitter: runtimeevents.Emitter{Sink: o.ports.Events, App: "tradecraft", SessionID: string(in.Binding.ConversationID), Sequencer: o.ports.Sequencer, Now: o.ports.Now}, base: EventPayload{RunID: in.ID, Binding: in.Binding, Environment: in.Environment.Receipt, DefinitionDigest: o.digest, Route: route.Name, Model: route.Model}}
	defer func() {
		if recover() != nil {
			runErr = ErrEffectPanic
		}
		switch {
		case ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded):
			result.Status = Cancelled
		case errors.Is(runErr, ErrPending):
			result.Status = Pending
		case runErr != nil:
			result.Status = Failed
		default:
			result.Status = Succeeded
		}
		terminalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		kind := runtimeevents.EventKind("sdk.run.completed")
		if runErr != nil {
			kind = "sdk.run.failed"
		}
		if e := events.emit(terminalCtx, kind, EventPayload{Status: result.Status, Usage: &result.Usage}); e != nil {
			runErr = errors.Join(runErr, e)
			if result.Status == Succeeded {
				result.Status = Failed
			}
		}
	}()
	if err = events.emit(ctx, "sdk.run.started", EventPayload{}); err != nil {
		return result, err
	}
	if err = o.verify(ctx, in); err != nil {
		return result, err
	}
	reservation, err := reserveDescriptor(in, o.digest, route)
	if err != nil {
		return result, err
	}
	snapshot, err := clone(reservation)
	if err != nil {
		return result, err
	}
	if err = o.ports.Admission.ReserveRun(ctx, snapshot); err != nil {
		return result, err
	}
	window := agentcontext.NewContextWindow(route.Provider.Capabilities().ContextWindowSize, nil)
	if err = o.prepare(ctx, in, reservation, window); err != nil {
		return result, err
	}
	// Negotiate once-only gate capabilities before the first model effect.
	if contains(o.required, CapabilityApproval) {
		caps, e := o.ports.Gate.Port.Capabilities(ctx, NativeAdapterScope())
		if e != nil {
			return result, e
		}
		if caps.Adapter != NativeAdapterScope() || !contains(caps.OperationClasses, "tool") || !contains(caps.ScopeKinds, gate.Once) || !contains(caps.DecisionKinds, gate.Allow) || !contains(caps.DecisionKinds, gate.Deny) {
			return result, ErrUnsupported
		}
	}
	messages := []llmtypes.ChatMessage{{Role: "user", Content: in.Text}}
	seen := map[string]bool{}
	var currentTurn runtimecontract.TurnID
	var selected map[string]Tool
	fail := func(err error) runloop.Directive { runErr = err; return runloop.Terminate }
	_, err = runloop.Execute(ctx, runloop.Ports[llmtypes.ChatRequest, turn.TurnResult]{
		Request: func(c context.Context) (llmtypes.ChatRequest, runloop.Directive) {
			if err := c.Err(); err != nil {
				return llmtypes.ChatRequest{}, fail(context.Cause(c))
			}
			if result.Turns >= o.ports.Limits.MaxTurns {
				return llmtypes.ChatRequest{}, fail(ErrLimit)
			}
			if maximum := route.Provider.Capabilities().MaxTokens; maximum > 0 && o.ports.Limits.MaxOutputTokens > maximum {
				return llmtypes.ChatRequest{}, fail(fmt.Errorf("%w: requested output exceeds provider limit", ErrUnsupported))
			}
			currentTurn = runtimecontract.TurnID(fmt.Sprintf("%s/turn/%d", in.ID, result.Turns+1))
			selected = map[string]Tool{}
			for name, tool := range o.tools {
				selected[name] = tool
			}
			if o.ports.Broker != nil {
				selection, e := o.ports.Broker.SelectTools(c, in.Text, nil)
				if e != nil {
					return llmtypes.ChatRequest{}, fail(e)
				}
				if selection == nil {
					return llmtypes.ChatRequest{}, fail(ErrInvalid)
				}
				selected = map[string]Tool{}
				for _, t := range selection.Tools {
					tool, ok := o.tools[t.Name]
					if !ok {
						return llmtypes.ChatRequest{}, fail(ErrUnsupported)
					}
					selected[t.Name] = tool
				}
				// Use registered schemas only. Cosmetic broker overrides are not authority
				// or instructions and are intentionally not injected by this facade.
			}
			names := make([]string, 0, len(selected))
			for name := range selected {
				names = append(names, name)
			}
			sort.Strings(names)
			request := llmtypes.ChatRequest{Model: route.Model, Messages: messages, Tools: toolDefinitions(selected, names), MaxTokens: o.ports.Limits.MaxOutputTokens}
			for _, block := range window.Assemble() {
				request.SlotBlocks = append(request.SlotBlocks, llmtypes.SlotBlock{Name: block.SlotName, Content: block.Content, CacheKey: block.CacheKey, Changed: block.Changed})
			}
			if e := checkContext(window, request); e != nil {
				return llmtypes.ChatRequest{}, fail(e)
			}
			request, e := clone(request)
			if e != nil {
				return llmtypes.ChatRequest{}, fail(e)
			}
			return request, runloop.Proceed
		},
		Consume: func(c context.Context, request llmtypes.ChatRequest) (out turn.TurnResult, directive runloop.Directive) {
			result.Turns++
			if e := events.emit(c, runtimeevents.KindTurnStarted, EventPayload{TurnID: currentTurn}); e != nil {
				return out, fail(e)
			}
			e := o.admit(c, Admission{Run: reservation, Kind: "model", Binding: in.Binding, Environment: in.Environment, DefinitionDigest: o.digest, Route: route.Name}, func(admitted context.Context) error {
				var e error
				out, e = turn.ExecuteTurn(admitted, turn.TurnRequest{Stream: func(streamCtx context.Context) (<-chan llmtypes.StreamEvent, error) {
					return route.Provider.StreamChat(streamCtx, request)
				}, IdleTimeout: o.ports.Limits.IdleTimeout, RequireTerminal: true})
				return e
			})
			if out.Usage != nil {
				result.Usage.InputTokens += out.Usage.InputTokens
				result.Usage.OutputTokens += out.Usage.OutputTokens
				result.Usage.CacheCreationTokens += out.Usage.CacheCreationTokens
				result.Usage.CacheReadTokens += out.Usage.CacheReadTokens
				result.Usage.StopReason = out.StopReason
			}
			if e != nil {
				observeCtx, cancel := context.WithTimeout(context.WithoutCancel(c), 5*time.Second)
				defer cancel()
				emitErr := events.emit(observeCtx, runtimeevents.KindTurnFailed, EventPayload{TurnID: currentTurn, Usage: out.Usage})
				return out, fail(errors.Join(e, emitErr))
			}
			if e = events.emit(c, runtimeevents.KindTurnCompleted, EventPayload{TurnID: currentTurn, Usage: out.Usage}); e != nil {
				return out, fail(e)
			}
			return out, runloop.Proceed
		},
		Settle: func(c context.Context, out turn.TurnResult) runloop.Directive {
			result.Text = out.Text
			switch llmtypes.NormalizeStopReason(out.StopReason) {
			case llmtypes.StopReasonMaxTokens, llmtypes.StopReasonTurnLimit:
				return fail(ErrLimit)
			case llmtypes.StopReasonCancelled:
				return fail(context.Canceled)
			case llmtypes.StopReasonError:
				return fail(fmt.Errorf("%w: failed model completion", ErrInvalid))
			}
			if len(out.ToolUseBlocks) == 0 {
				if llmtypes.NormalizeStopReason(out.StopReason) == llmtypes.StopReasonToolUse {
					return fail(fmt.Errorf("%w: incoherent model completion", ErrInvalid))
				}
				return runloop.Finish
			}
			// Validate the whole tool turn before the first effect. Duplicate IDs cannot
			// obtain another operation identity/claim, including across later turns.
			for _, call := range out.ToolUseBlocks {
				if strings.TrimSpace(call.ID) == "" || seen[call.ID] {
					return fail(fmt.Errorf("%w: empty or reused tool call ID", ErrInvalid))
				}
				if _, ok := selected[call.Name]; !ok {
					return fail(fmt.Errorf("%w: unadvertised tool", ErrUnsupported))
				}
				seen[call.ID] = true
			}
			assistant := llmtypes.ChatMessage{Role: "assistant"}
			for _, thinking := range out.ThinkingBlocks {
				assistant.ContentBlocks = append(assistant.ContentBlocks, llmtypes.ContentBlock{Type: "thinking", Text: thinking.Thinking, Signature: thinking.Signature})
			}
			if out.Text != "" {
				assistant.ContentBlocks = append(assistant.ContentBlocks, llmtypes.ContentBlock{Type: "text", Text: out.Text})
			}
			for _, call := range out.ToolUseBlocks {
				args := call.Input
				assistant.ContentBlocks = append(assistant.ContentBlocks, llmtypes.ContentBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: &args})
			}
			messages = append(messages, assistant)
			toolResults := llmtypes.ChatMessage{Role: "user"}
			for _, call := range out.ToolUseBlocks {
				toolResult, e := o.executeTool(c, in, reservation, currentTurn, call, selected[call.Name], &events, &result)
				if e != nil {
					return fail(e)
				}
				toolResults.ContentBlocks = append(toolResults.ContentBlocks, llmtypes.ContentBlock{Type: "tool_result", ToolUseID: call.ID, Content: toolResult.Content, IsError: toolResult.IsError})
			}
			messages = append(messages, toolResults)
			return runloop.Continue
		},
		Finalize: func(c context.Context) runloop.Directive {
			if e := c.Err(); e != nil {
				return fail(context.Cause(c))
			}
			return runloop.Finish
		},
	})
	if err != nil {
		runErr = errors.Join(runErr, err)
	}
	return result, runErr
}

func contains[T comparable](values []T, v T) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func reserveDescriptor(in Input, definitionDigest string, route Route) (RunReservation, error) {
	raw, err := json.Marshal(struct {
		Input            Input
		DefinitionDigest string
		Route, Model     string
	}{in, definitionDigest, route.Name, route.Model})
	if err != nil {
		return RunReservation{}, err
	}
	hash := sha256.Sum256(raw)
	return RunReservation{RunID: in.ID, Binding: in.Binding, Environment: in.Environment, DefinitionDigest: definitionDigest, Route: route.Name, Model: route.Model, InputDigest: runtimecontract.Digest{Scheme: "sha256", DescriptorVersion: "sdk-run-input-v1", Value: hex.EncodeToString(hash[:])}}, nil
}
