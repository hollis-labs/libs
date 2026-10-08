package turn

import (
	"context"
	"errors"
	"fmt"
	"time"

	llmtypes "github.com/hollis-labs/go-llm-types"
)

// ErrTurnTruncated distinguishes transport EOF from a provider terminal event.
var ErrTurnTruncated = errors.New("upstream provider stream ended without a terminal signal")

// Reader owns stream cancellation observation and the provider inactivity
// watchdog. It starts no goroutines. Next and Close are used by one consumer;
// the host retains responsibility for cancelling the provider on every exit.
type Reader struct {
	ctx      context.Context
	events   <-chan llmtypes.StreamEvent
	idle     time.Duration
	timer    *time.Timer
	closed   bool
	terminal bool
}

func NewReader(ctx context.Context, events <-chan llmtypes.StreamEvent, idle time.Duration) (*Reader, error) {
	if idle <= 0 {
		return nil, &TurnError{Kind: TurnErrorStart, Err: errors.New("idle timeout must be positive")}
	}
	return &Reader{ctx: ctx, events: events, idle: idle, timer: time.NewTimer(idle)}, nil
}

// TerminalSeen records only the normalized provider EventDone signal. Channel
// closure and usage stop reasons do not establish canonical turn completion.
func (r *Reader) TerminalSeen() bool { return r.terminal }

func (r *Reader) Close() {
	if !r.closed {
		r.closed = true
		r.timer.Stop()
	}
}

// Next gives caller cancellation precedence over a concurrently ready event
// or EOF. Idle time is measured between events, excluding tool execution.
func (r *Reader) Next() (llmtypes.StreamEvent, bool, error) {
	var zero llmtypes.StreamEvent
	if err := r.ctx.Err(); err != nil {
		r.Close()
		return zero, false, &TurnError{Kind: TurnErrorStream, Err: context.Cause(r.ctx)}
	}
	if r.closed {
		return zero, false, nil
	}
	select {
	case event, ok := <-r.events:
		if err := r.ctx.Err(); err != nil {
			r.Close()
			return zero, false, &TurnError{Kind: TurnErrorStream, Err: context.Cause(r.ctx)}
		}
		if !ok {
			r.Close()
			return zero, false, nil
		}
		if !r.timer.Stop() {
			select {
			case <-r.timer.C:
			default:
			}
		}
		r.timer.Reset(r.idle)
		if event.Type == llmtypes.EventDone {
			r.terminal = true
		}
		return event, true, nil
	case <-r.timer.C:
		r.Close()
		return zero, false, &TurnError{Kind: TurnErrorStalled, Err: fmt.Errorf("%w: no provider events for %s", ErrTurnStalled, r.idle)}
	case <-r.ctx.Done():
		r.Close()
		return zero, false, &TurnError{Kind: TurnErrorStream, Err: context.Cause(r.ctx)}
	}
}
