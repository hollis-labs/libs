package hosttest

import (
	"context"
	"errors"
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
			return errors.New("first bridge call did not enter")
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
			return errors.New("second bridge call did not enter")
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
			return errors.New("cancel_call not admitted")
		}
		select {
		case <-f.Gate.Cancelled:
		case <-ctx.Done():
			return errors.New("cancel_call did not cancel selected request")
		}
		select {
		case <-f.SecondGate.Cancelled:
			return errors.New("cancel_call cancelled unrelated request")
		default:
		}
		if s := o.Snapshot(); s.Reserved-s.Released != 2 {
			return errors.New("cancel_call released running reservations")
		}
		f.Gate.Release()
		f.SecondGate.Release()
		select {
		case got := <-firstDone:
			if got.err != nil {
				return got.err
			}
			if got.reply.Failure == nil || got.reply.Failure.Code != capability.Cancelled || got.reply.Failure.EffectState != capability.NotStarted {
				return errors.New("cancelled bridge outcome unsafe")
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
				return errors.New("unrelated call refused")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		s := o.Snapshot()
		if s.Executions != 2 || s.Reserved != s.Released {
			return errors.New("cancelled request executed or leaked reservation")
		}
		c.Call.RequestID = 4
		return allowed(ctx, i, o, c)
	})
}
