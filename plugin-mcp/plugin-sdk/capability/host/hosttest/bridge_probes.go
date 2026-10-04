package hosttest

import (
	"context"

	"github.com/hollis-labs/plugin-sdk/capability"
)

func bridgeProbes() []probe {
	out := []probe{}
	for _, op := range []string{"host/mcp/list_tools", "host/mcp/call_tool"} {
		out = append(out, probe{"C07", "bridge admitted " + op, func(ctx context.Context, a Adapter) error {
			f, c := basic(capability.MCPReach)
			c.Call.Operation = op
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				c.BindingID = ""
				_, c.Credential = i.Access()
				c.Bridge = true
				return allowed(ctx, i, o, c)
			})
		}})
	}
	out = append(out, probe{"C07", "cancel_call leaves concurrent request running", bridgeCancelCall})
	out = append(out, probe{"C07", "cancel_call cannot cross actors", bridgeCrossActorCancel})
	return out
}
func bridgeCancelCall(ctx context.Context, a Adapter) error {
	f, c := basic(capability.MCPReach)
	f.Gate = NewGate()
	f.SecondGate = NewGate()
	defer f.Gate.Release()
	defer f.SecondGate.Release()
	return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
		c.BindingID = ""
		_, c.Credential = i.Access()
		c.Bridge = true
		type result struct {
			reply Reply
			err   error
		}
		firstDone := make(chan result, 1)
		secondDone := make(chan result, 1)
		go func() { r, e := invokeAsync(ctx, i, c); firstDone <- result{r, e} }()
		select {
		case <-f.Gate.Entered:
		case r := <-firstDone:
			if r.err != nil {
				return r.err
			}
			return suiteError("first bridge call did not enter")
		case <-ctx.Done():
			return ctx.Err()
		}
		second := c
		second.Call.RequestID = 3
		go func() { r, e := invokeAsync(ctx, i, second); secondDone <- result{r, e} }()
		select {
		case <-f.SecondGate.Entered:
		case r := <-secondDone:
			if r.err != nil {
				return r.err
			}
			return suiteError("second bridge call did not enter")
		case <-ctx.Done():
			return ctx.Err()
		}
		cancel := c
		cancel.Call.RequestID = 2
		cancel.Call.Operation = "host/mcp/cancel_call"
		cancel.CancelID = 1
		before := o.Snapshot()
		r, err := i.Invoke(ctx, cancel)
		if err != nil {
			return err
		}
		if r.Failure != nil || o.Snapshot().Executions != before.Executions+1 {
			return suiteError("cancel_call not admitted")
		}
		select {
		case <-f.Gate.Cancelled:
		case <-ctx.Done():
			return suiteError("cancel_call did not cancel selected request")
		}
		select {
		case <-f.SecondGate.Cancelled:
			return suiteError("cancel_call cancelled unrelated request")
		default:
		}
		if s := o.Snapshot(); s.Reserved-s.Released != 2 {
			return suiteError("cancel_call released running reservations")
		}
		f.Gate.Release()
		f.SecondGate.Release()
		select {
		case got := <-firstDone:
			if got.err != nil {
				return got.err
			}
			if got.reply.Failure == nil || got.reply.Failure.Code != capability.Cancelled || got.reply.Failure.EffectState != capability.NotStarted {
				return suiteError("cancelled bridge outcome unsafe")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case got := <-secondDone:
			if got.err != nil {
				return got.err
			}
			if got.reply.Failure != nil {
				return suiteError("unrelated call refused")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		s := o.Snapshot()
		if s.Executions != 2 || s.Reserved != s.Released {
			return suiteError("cancelled request executed or leaked reservation")
		}
		c.Call.RequestID = 4
		return allowed(ctx, i, o, c)
	})
}

func bridgeCrossActorCancel(ctx context.Context, a Adapter) error {
	f, c := basic(capability.MCPReach)
	f.Gate = NewGate()
	defer f.Gate.Release()
	return withActive(ctx, a, f, c, func(i Instance, o *Observer, plugin Attempt) error {
		session := plugin
		session.BindingID = ""
		_, session.Credential = i.Access()
		session.Bridge = true
		type result struct {
			r Reply
			e error
		}
		done := make(chan result, 1)
		go func() { r, e := invokeAsync(ctx, i, session); done <- result{r, e} }()
		select {
		case <-f.Gate.Entered:
		case <-done:
			return suiteError("session call did not enter")
		case <-ctx.Done():
			return ctx.Err()
		}
		cancel := plugin
		cancel.Call.RequestID = 9
		cancel.Call.Operation = "host/mcp/cancel_call"
		cancel.CancelID = session.Call.RequestID
		if err := allowed(ctx, i, o, cancel); err != nil {
			return err
		}
		f.Gate.Release()
		select {
		case got := <-done:
			if got.e != nil {
				return got.e
			}
			if got.r.Failure != nil {
				return suiteError("another actor cancelled session call")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-f.Gate.Cancelled:
			return suiteError("cross-actor cancellation delivered")
		default:
		}
		s := o.Snapshot()
		if s.Executions != 2 || s.Reserved != s.Released {
			return suiteError("cross-actor cancel affected work or reservations")
		}
		return nil
	})
}
